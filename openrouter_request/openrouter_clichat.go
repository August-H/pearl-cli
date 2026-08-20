package openrouter_request

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
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Settings struct {
	Username                 string   `json:"username"`
	Model                    string   `json:"model"`
	Max_concurrency          int      `json:"max_concurrency"`
	Max_depth                int      `json:"max_depth"`
	Mode                     string   `json:"mode"`
	Max_job_seconds          int      `json:"max_job_seconds"`
	Max_file_bytes           int64    `json:"max_file_bytes"`
	Approved_workspace_roots []string `json:"approved_workspace_roots"`
}
type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning"`
}
type Choices struct {
	Index   int     `json:"index"`
	Message Message `json:"message"`
}
type Response struct {
	Id      string    `json:"id"`
	Model   string    `json:"model"`
	Choices []Choices `json:"choices"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Reasoning struct {
	Enabled bool `json:"enabled"`
}
type ChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Reasoning Reasoning     `json:"reasoning"`
	Stream    bool          `json:"stream"`
}

type APIError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Error   *APIError      `json:"error,omitempty"`
}

func Openrouter_clichat() {
	fmt.Println("Enter your message!")
	err := godotenv.Load()
	openrouterAPIKey := os.Getenv("OPENROUTER_API_KEY")

	if openrouterAPIKey == "" {
		log.Fatal(errors.New("OPENROUTER_API_KEY is not set"))
	}

	settings_file, err := os.Open("settings.json")

	if err != nil {
		log.Fatal(errors.New("Could not read settings file"))
	}
	var settings Settings
	err = json.NewDecoder(settings_file).Decode(&settings)

	scanner := bufio.NewScanner(os.Stdin)

	var content string
	scanner.Scan()
	content = scanner.Text()

	if content == "" {
		fmt.Println("No content captured")
		return
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reading standard input:", err)
	}

	var messages []ChatMessage

	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: content,
	})

	reasoning := Reasoning{
		Enabled: true,
	}

	payload := ChatRequest{
		Model:     settings.Model,
		Messages:  messages,
		Reasoning: reasoning,
		Stream:    true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://openrouter.ai/api/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+openrouterAPIKey)

	client := http.Client{
		Timeout: time.Minute,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			log.Fatalf("OpenRouter returned %s (failed to read body: %v)", resp.Status, readErr)
		}
		log.Fatalf("OpenRouter returned %s: %s", resp.Status, responseBody)
	}

	streamScanner := bufio.NewScanner(resp.Body)
	streamScanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for streamScanner.Scan() {
		line := streamScanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}

		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			fmt.Println()
			log.Fatalf("Could not decode OpenRouter stream: %v", err)
		}

		if chunk.Error != nil {
			fmt.Println()
			log.Fatalf("OpenRouter stream error (%v): %s", chunk.Error.Code, chunk.Error.Message)
		}

		for _, choice := range chunk.Choices {
			fmt.Print(choice.Delta.Content)
		}
	}

	if err := streamScanner.Err(); err != nil {
		fmt.Println()
		log.Fatalf("Error reading OpenRouter stream: %v", err)
	}

}
