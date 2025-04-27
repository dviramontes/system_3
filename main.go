package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
)

func main() {
	client := anthropic.NewClient()

	scanner := bufio.NewScanner(os.Stdin)
	getUserMessage := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}

		return scanner.Text(), true
	}

	agent := NewAgent(&client, getUserMessage)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

func NewAgent(client *anthropic.Client, getUserMessage func() (string, bool)) *Agent {
	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
	}
}

type Agent struct {
	client         *anthropic.Client
	getUserMessage func() (string, bool)
}

func (a *Agent) Run(ctc context.Context) error {
	var conversation []anthropic.MessageParam

	fmt.Println("Chat with Claude (press Ctrl+C to exit)")

	for {
		fmt.Print("\u001b[94mYou\u001b[0m: ")
		userInput, ok := a.getUserMessage()
		if !ok {
			break
		}

		userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
		conversation = append(conversation, userMessage)

		message, err := a.runInterface(ctc, conversation)
		if err != nil {
			return err
		}
		conversation = append(conversation, message.ToParam())

		for _, content := range message.Content {
			switch content.Type {
			case "text":
				fmt.Printf("\u001b[92mClaude\u001b[0m: %s\n", content.Text)
			}
		}

	}

	return nil
}

func (a *Agent) runInterface(ctc context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	return a.client.Messages.New(ctc, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaude3_7SonnetLatest,
		MaxTokens: int64(1024),
		Messages:  conversation,
	})
}
