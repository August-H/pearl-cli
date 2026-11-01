package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/chatgpt"
	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const authUsage = `Usage:
  pearl auth login chatgpt [--no-browser] [--new-account | --account <id>]
  pearl auth login claude
  pearl auth login antigravity
  pearl auth status
  pearl auth accounts
  pearl auth use <id>
  pearl auth logout [claude | antigravity]

Each sign-in adds its models to pearl model: GPT from ChatGPT, Claude from your
installed Claude Code, and Gemini from your installed Antigravity server.

Continue with ChatGPT to use an eligible ChatGPT plan in Pearl.
Login uses your browser and stores credentials in Pearl's config directory.
Use --no-browser to open the printed URL yourself on this machine. For SSH,
forward the callback port shown in redirect_uri to the machine running Pearl.
Manage app usage and access at https://chatgpt.com/#settings/Usage`

var newChatGPTClient = chatgpt.NewClient
var openAuthBrowser = func(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	return command.Run()
}

func runAuth(args []string) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println(authUsage)
		return 0
	}
	if len(args) == 2 && (args[0] == "login" || args[0] == "logout") {
		switch args[1] {
		case "claude":
			return claudeAuthCommand(args[0])
		case "antigravity":
			return runAntigravity(args[:1])
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 5*time.Minute)
	defer timeout()
	client := newChatGPTClient()
	switch args[0] {
	case "login":
		if len(args) < 2 || args[1] != "chatgpt" {
			fmt.Fprintln(os.Stderr, authUsage)
			return 2
		}
		noBrowser, newAccount, accountID := false, false, ""
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--no-browser":
				noBrowser = true
			case "--new-account":
				newAccount = true
			case "--account":
				if i+1 >= len(args) {
					fmt.Fprintln(os.Stderr, authUsage)
					return 2
				}
				i++
				accountID = args[i]
			default:
				fmt.Fprintln(os.Stderr, authUsage)
				return 2
			}
		}
		if newAccount && accountID != "" {
			fmt.Fprintln(os.Stderr, "Use either --new-account or --account")
			return 2
		}
		if !newAccount && accountID == "" {
			accounts, err := client.Accounts(ctx)
			if err != nil {
				return printError("ChatGPT", err)
			}
			for _, a := range accounts {
				if a.Active {
					accountID = a.ClientID
				}
			}
		}
		fmt.Println("Continue with ChatGPT. Allow Pearl to use your ChatGPT plan in the browser.")
		fmt.Println("Your plan's usage limits apply. Manage usage:", chatgpt.UsageURL)
		err := client.Login(ctx, accountID, func(address string) error {
			fmt.Println("Open this URL to sign in:\n" + address)
			if !noBrowser {
				if err := openAuthBrowser(address); err != nil {
					fmt.Println("Could not open a browser. Open the URL above manually.")
				}
			}
			return nil
		})
		if err != nil {
			return printError("ChatGPT", err)
		}
		forgetModelCatalogs()
		if err := activateChatGPT(ctx, client); err != nil {
			return printError("ChatGPT linked; provider setup", err)
		}
		fmt.Println("ChatGPT connected. Pearl now uses your ChatGPT plan.")
		return printAuthStatus(ctx, client, false)
	case "status", "accounts":
		if len(args) != 1 {
			break
		}
		return printAuthStatus(ctx, client, args[0] == "accounts")
	case "use":
		if len(args) != 2 {
			break
		}
		if err := client.Select(ctx, args[1]); err != nil {
			return printError("ChatGPT", err)
		}
		forgetModelCatalogs()
		if err := activateChatGPT(ctx, client); err != nil {
			return printError("ChatGPT", err)
		}
		return printAuthStatus(ctx, client, false)
	case "logout":
		if len(args) != 1 {
			break
		}
		if err := client.Logout(ctx); err != nil {
			return printError("ChatGPT", err)
		}
		forgetModelCatalogs()
		fmt.Println("Signed out of the selected ChatGPT account. Run `pearl auth login chatgpt` to reconnect.")
		return 0
	}
	fmt.Fprintln(os.Stderr, authUsage)
	return 2
}

func activateChatGPT(ctx context.Context, client *chatgpt.Client) error {
	models, err := client.Models(ctx)
	if err != nil {
		return err
	}
	settings, _ := openrouter_request.LoadAgentSettings()
	chosen := models[0].ID
	for _, m := range models {
		if m.ID == settings.ChatGPTModel {
			chosen = m.ID
		}
	}
	if _, err := openrouter_request.ConfigureChatGPTModel(chosen); err != nil {
		return err
	}
	_, err = openrouter_request.ConfigureProvider(openrouter_request.ProviderChatGPT)
	return err
}

func printAuthStatus(ctx context.Context, client *chatgpt.Client, all bool) int {
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return printError("ChatGPT", err)
	}
	found := false
	for _, account := range accounts {
		if !all && !account.Active {
			continue
		}
		found = true
		marker := " "
		if account.Active {
			marker = "*"
		}
		status := "signed out"
		if account.Connected {
			status = "connected"
		}
		fmt.Printf("%s %s  %s  %s\n", marker, account.ClientID, account.Email, status)
		if account.Connected && account.PlanUsage {
			fmt.Println("  ChatGPT plan usage is enabled.")
		}
	}
	if !found {
		fmt.Println("ChatGPT is not connected. Run `pearl auth login chatgpt`.")
	}
	fmt.Println("Claude Code:", connectionStatus(checkClaudeSignIn(ctx), "claude"))
	fmt.Println("Antigravity:", connectionStatus(daemon.AntigravitySignedIn(), "antigravity"))
	settings, _ := openrouter_request.LoadAgentSettings()
	fmt.Println("Billing:", billingLabel(daemon.BillingFor(openrouter_request.AgentProvider(settings))))
	if model := openrouter_request.AgentModel(settings); model != "" {
		fmt.Println("Model:", modelLabel(model))
	}
	fmt.Println("Manage usage:", chatgpt.UsageURL)
	return 0
}

func connectionStatus(signedIn bool, login string) string {
	if signedIn {
		return "signed in"
	}
	return "not signed in. Run `pearl auth login " + login + "`."
}

func runProvider(args []string) int {
	if len(args) == 0 {
		settings, _ := openrouter_request.LoadAgentSettings()
		fmt.Println("Provider:", openrouter_request.AgentProvider(settings))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: pearl provider [openrouter|chatgpt]")
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: pearl provider [openrouter|chatgpt]")
		return 2
	}
	provider := strings.ToLower(strings.TrimSpace(args[0]))
	if provider == openrouter_request.ProviderClaude || provider == openrouter_request.ProviderAntigravity {
		return printError("Provider", fmt.Errorf("choose a %s model with `pearl model` instead", provider))
	}
	if provider == openrouter_request.ProviderChatGPT {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := activateChatGPT(ctx, newChatGPTClient()); err != nil {
			return printError("Provider", err)
		}
	} else if _, err := openrouter_request.ConfigureProvider(provider); err != nil {
		return printError("Provider", err)
	}
	fmt.Println("Provider:", provider)
	return 0
}
