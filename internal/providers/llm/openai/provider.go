package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/llm"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Config struct {
	APIKey  string
	Model   string
	BaseURL string
}

type Provider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
	requests   atomic.Uint64
}

func New(config Config) (*Provider, error) {
	if config.APIKey == "" {
		return nil, errors.New("OpenAI API key is required")
	}

	if config.Model == "" {
		config.Model = "gpt-5.6-luna"
	}

	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}

	return &Provider{
		apiKey:     config.APIKey,
		model:      config.Model,
		baseURL:    strings.TrimRight(config.BaseURL, "/"),
		httpClient: &http.Client{},
	}, nil
}

func (p *Provider) Name() string {
	return "openai"
}

type responsesRequest struct {
	Model        string         `json:"model"`
	Input        []inputMessage `json:"input"`
	Stream       bool           `json:"stream"`
	Instructions string         `json:"instructions,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p *Provider) Generate(
	ctx context.Context,
	req llm.Request,
) (llm.Stream, error) {
	requestID := p.requests.Add(1)
	requestStart := time.Now()
	diagnostic := strings.EqualFold(os.Getenv("NEMOTRON_PERFORMANCE"), "true")
	messages := make(
		[]inputMessage,
		0,
		len(req.Messages),
	)

	for _, message := range req.Messages {
		messages = append(
			messages,
			inputMessage{
				Role:    message.Role,
				Content: message.Content,
			},
		)
	}

	body, err := json.Marshal(
		responsesRequest{
			Model:  p.model,
			Input:  messages,
			Stream: true,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"encode OpenAI request: %w",
			err,
		)
	}
	encodedAt := time.Now()

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.baseURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create OpenAI request: %w",
			err,
		)
	}

	httpRequest.Header.Set(
		"Authorization",
		"Bearer "+p.apiKey,
	)

	httpRequest.Header.Set(
		"Content-Type",
		"application/json",
	)

	httpRequest.Header.Set(
		"Accept",
		"text/event-stream",
	)
	var gotConnAt, firstByteAt time.Time
	var reused bool
	if diagnostic {
		trace := &httptrace.ClientTrace{
			GotConn:              func(info httptrace.GotConnInfo) { gotConnAt, reused = time.Now(), info.Reused },
			GotFirstResponseByte: func() { firstByteAt = time.Now() },
		}
		httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), trace))
	}

	response, err :=
		p.httpClient.Do(httpRequest)

	if err != nil {
		return nil, fmt.Errorf(
			"send OpenAI request: %w",
			err,
		)
	}
	responseAt := time.Now()
	if diagnostic {
		log.Printf("OpenAI diagnostic: request=%d context_items=%d body_bytes=%d encode_ms=%.3f connection_reused=%v got_connection_ms=%.3f first_byte_ms=%.3f headers_ms=%.3f", requestID, len(req.Messages), len(body), float64(encodedAt.Sub(requestStart).Microseconds())/1000, reused, durationMilliseconds(gotConnAt, requestStart), durationMilliseconds(firstByteAt, requestStart), float64(responseAt.Sub(requestStart).Microseconds())/1000)
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		defer response.Body.Close()

		errorBody, _ :=
			io.ReadAll(
				io.LimitReader(
					response.Body,
					64*1024,
				),
			)

		return nil, fmt.Errorf(
			"OpenAI API returned %s: %s",
			response.Status,
			strings.TrimSpace(
				string(errorBody),
			),
		)
	}

	return &responseStream{
		body:       response.Body,
		scanner:    bufio.NewScanner(response.Body),
		requestID:  requestID,
		started:    requestStart,
		diagnostic: diagnostic,
	}, nil
}

func durationMilliseconds(value, start time.Time) float64 {
	if value.IsZero() {
		return -1
	}
	return float64(value.Sub(start).Microseconds()) / 1000
}

type responseStream struct {
	body       io.ReadCloser
	scanner    *bufio.Scanner
	requestID  uint64
	started    time.Time
	firstDelta bool
	textBytes  int
	diagnostic bool
}

type streamEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`

	Response struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

func (s *responseStream) Recv() (
	llm.Delta,
	error,
) {
	for s.scanner.Scan() {
		line := s.scanner.Text()

		if !strings.HasPrefix(
			line,
			"data:",
		) {
			continue
		}

		data := strings.TrimSpace(
			strings.TrimPrefix(
				line,
				"data:",
			),
		)

		if data == "" {
			continue
		}

		if data == "[DONE]" {
			return llm.Delta{}, io.EOF
		}

		var event streamEvent

		if err := json.Unmarshal(
			[]byte(data),
			&event,
		); err != nil {
			return llm.Delta{}, fmt.Errorf(
				"decode OpenAI stream event: %w",
				err,
			)
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == "" {
				continue
			}

			s.textBytes += len([]byte(event.Delta))
			if !s.firstDelta {
				s.firstDelta = true
				if s.diagnostic {
					log.Printf("OpenAI diagnostic: request=%d stage=first_delta duration_ms=%.3f", s.requestID, float64(time.Since(s.started).Microseconds())/1000)
				}
			}
			return llm.Delta{
				Text: event.Delta,
			}, nil

		case "response.completed":
			if s.diagnostic {
				log.Printf("OpenAI diagnostic: request=%d stage=complete duration_ms=%.3f text_bytes=%d", s.requestID, float64(time.Since(s.started).Microseconds())/1000, s.textBytes)
			}
			return llm.Delta{}, io.EOF

		case "response.failed":
			if event.Response.Error != nil {
				return llm.Delta{}, fmt.Errorf(
					"OpenAI response failed: %s: %s",
					event.Response.Error.Code,
					event.Response.Error.Message,
				)
			}

			return llm.Delta{},
				errors.New(
					"OpenAI response failed",
				)
		}
	}

	if err := s.scanner.Err(); err != nil {
		return llm.Delta{}, fmt.Errorf(
			"read OpenAI stream: %w",
			err,
		)
	}

	return llm.Delta{}, io.EOF
}

func (s *responseStream) Close() error {
	return s.body.Close()
}
