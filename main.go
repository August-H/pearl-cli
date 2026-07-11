package main

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
	"time"

	"github.com/joho/godotenv"
)

type Settings struct {
	Username        string `json:"username"`
	Model           string `json:"model"`
	Max_concurrency int    `json:"max_concurrency"`
	Max_depth       int    `json:"max_depth"`
	Mode            string `json:"mode"`
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
	Streaming bool          `json:"stream"`
	Messages  []ChatMessage `json:"messages"`
	Reasoning Reasoning     `json:"reasoning"`
}

func main() {
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
	fmt.Printf("Captured: %s\n", content)

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "reading standard input:", err)
	}

	var messages []ChatMessage

	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: content,
	})

	reasoning := Reasoning{
		Enabled: false,
	}

	payload := ChatRequest{
		Model:     settings.Model,
		Streaming: true,
		Messages:  messages,
		Reasoning: reasoning,
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

	var response Response
	json.NewDecoder(resp.Body).Decode(&response)
	if len(response.Choices) != 0 {
		fmt.Println(response.Choices[0].Message.Content)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			log.Fatalf("OpenRouter returned %s (failed to read body: %v)", resp.Status, readErr)
		}
		log.Fatalf("OpenRouter returned %s: %s", resp.Status, responseBody)
	}

}
