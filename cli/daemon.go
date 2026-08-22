package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/August-H/pearl-cli/internal/daemon"
	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/openrouter_request"
)

const serviceLabel = "com.august.pearl"

func runDaemonCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: pearl daemon <run|start|stop|restart|status|install|uninstall>")
		return 2
	}
	switch args[0] {
	case "run":
		if _, err := openrouter_request.EnsureAgentSettings(); err != nil {
			return printError("Settings", err)
		}
		paths, err := pearlpaths.Resolve()
		if err != nil {
			return printError("Daemon", err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Println("Pearl daemon listening at", paths.Socket)
		if err := daemon.Run(ctx, paths, daemon.OpenRouterRunner{}); err != nil {
			return printError("Daemon", err)
		}
		return 0
	case "start":
		return startDaemon()
	case "stop":
		return stopDaemon()
	case "restart":
		return restartDaemon()
	case "status":
		return printDaemonStatus()
	case "install":
		if err := installService(); err != nil {
			return printError("Install", err)
		}
		return 0
	case "uninstall":
		if err := uninstallService(); err != nil {
			return printError("Uninstall", err)
		}
		return 0
	default:
		return printInvalidCommand("daemon " + args[0])
	}
}

func stopDaemon() int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Daemon", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = client.shutdown(ctx)
	cancel()
	if err != nil {
		return printError("Daemon", err)
	}
	fmt.Println("Pearl daemon is stopping")
	return 0
}

func restartDaemon() int {
	return restartDaemonWithStart(startDaemon)
}

func restartDaemonWithStart(start func() int) int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Daemon", err)
	}
	statusContext, statusCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	_, statusErr := client.status(statusContext)
	statusCancel()
	if statusErr == nil {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = client.shutdown(shutdownContext)
		shutdownCancel()
		if err != nil {
			return printError("Daemon", err)
		}
		fmt.Println("Pearl daemon is stopping")
		if err := waitForDaemonStop(client, 5*time.Second); err != nil {
			return printError("Daemon", err)
		}
	}
	return start()
}

func waitForDaemonStop(client *daemonClient, timeout time.Duration) error {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := client.status(ctx)
		cancel()
		if err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Pearl daemon did not stop")
}

func startDaemon() int {
	client, err := newDaemonClient()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, statusErr := client.status(ctx)
		cancel()
		if statusErr == nil {
			fmt.Println("Pearl daemon is already running")
			return 0
		}
	}
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return printError("Daemon", err)
	}
	if err := pearlpaths.Ensure(paths); err != nil {
		return printError("Daemon", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return printError("Daemon", err)
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return printError("Daemon", err)
	}
	if err := logFile.Chmod(0o600); err != nil {
		_ = logFile.Close()
		return printError("Daemon", err)
	}
	command := exec.Command(executable, "daemon", "run")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	configureDetachedProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return printError("Daemon", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()

	client, err = newDaemonClient()
	if err != nil {
		return printError("Daemon", err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err := client.status(ctx)
		cancel()
		if err == nil {
			fmt.Println("Pearl daemon started")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	return printError("Daemon", fmt.Errorf("did not start; inspect %s", paths.Log))
}

func printDaemonStatus() int {
	client, err := newDaemonClient()
	if err != nil {
		return printError("Status", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := client.status(ctx)
	if err != nil {
		fmt.Println("Pearl daemon is stopped")
		return 1
	}
	fmt.Println("Pearl daemon is running")
	if current, _ := status["current_job_id"].(string); current != "" {
		fmt.Println("Current job:", current)
	}
	if queued, ok := status["queued_jobs"].(float64); ok {
		fmt.Println("Queued jobs:", int(queued))
	}
	waiting, waitingOk := status["waiting_input_jobs"].(float64)
	if !waitingOk || int(waiting) == 0 {
		return 0
	}
	jobs, err := client.jobs(ctx)
	if err != nil {
		fmt.Println("Jobs waiting for input:", int(waiting))
		return 0
	}
	var waitingIDs []string
	for _, job := range jobs {
		if job.Status == store.JobWaitingInput {
			waitingIDs = append(waitingIDs, job.ID)
		}
	}
	const displayLimit = 3
	displayed := waitingIDs
	suffix := ""
	if len(waitingIDs) > displayLimit {
		displayed = waitingIDs[:displayLimit]
		suffix = fmt.Sprintf(", and %d more", len(waitingIDs)-displayLimit)
	}
	fmt.Println(
		"Waiting for input:", strings.Join(displayed, ", ")+suffix,
	)
	return 0
}

func installService() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return err
	}
	if err := pearlpaths.Ensure(paths); err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := logFile.Chmod(0o600); err != nil {
		_ = logFile.Close()
		return err
	}
	if err := logFile.Close(); err != nil {
		return err
	}
	if err := stopIdleDaemonForInstall(); err != nil {
		return err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		serviceDirectory := filepath.Join(home, "Library", "LaunchAgents")
		servicePath := filepath.Join(serviceDirectory, serviceLabel+".plist")
		if err := os.MkdirAll(serviceDirectory, 0o755); err != nil {
			return err
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>daemon</string><string>run</string></array>
<key>WorkingDirectory</key><string>%s</string>
<key>EnvironmentVariables</key><dict>
<key>PEARL_CONFIG_DIR</key><string>%s</string>
<key>PEARL_SOCKET</key><string>%s</string>
</dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, serviceLabel, html.EscapeString(executable), html.EscapeString(workingDirectory),
			html.EscapeString(paths.Directory), html.EscapeString(paths.Socket),
			html.EscapeString(paths.Log), html.EscapeString(paths.Log))
		if err := os.WriteFile(servicePath, []byte(plist), 0o644); err != nil {
			return err
		}
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+serviceLabel).Run()
		if output, err := exec.Command("launchctl", "bootstrap", domain, servicePath).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w: %s", err, output)
		}
		fmt.Println("Installed and started", servicePath)
		return nil
	case "linux":
		userConfig, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		serviceDirectory := filepath.Join(userConfig, "systemd", "user")
		servicePath := filepath.Join(serviceDirectory, "pearl.service")
		if err := os.MkdirAll(serviceDirectory, 0o755); err != nil {
			return err
		}
		unit := fmt.Sprintf(`[Unit]
Description=Pearl single-agent daemon

[Service]
ExecStart=%s daemon run
WorkingDirectory=%s
Environment=PEARL_CONFIG_DIR=%s
Environment=PEARL_SOCKET=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, strconv.Quote(executable), strconv.Quote(workingDirectory),
			strconv.Quote(paths.Directory), strconv.Quote(paths.Socket))
		if err := os.WriteFile(servicePath, []byte(unit), 0o644); err != nil {
			return err
		}
		if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w: %s", err, output)
		}
		if output, err := exec.Command("systemctl", "--user", "enable", "--now", "pearl.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable: %w: %s", err, output)
		}
		fmt.Println("Installed and started", servicePath)
		return nil
	default:
		return errors.New("service installation is supported on macOS and Linux")
	}
}

func stopIdleDaemonForInstall() error {
	client, err := newDaemonClient()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	status, err := client.status(ctx)
	cancel()
	if err != nil {
		return nil
	}
	if current, _ := status["current_job_id"].(string); current != "" {
		return fmt.Errorf("job %s is running; wait for it or cancel it before installing the service", current)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = client.shutdown(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("stop existing daemon before install: %w", err)
	}
	if err := waitForDaemonStop(client, 5*time.Second); err != nil {
		return fmt.Errorf("existing %w", err)
	}
	return nil
}

func uninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		servicePath := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+serviceLabel).Run()
		if err := os.Remove(servicePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("Uninstalled", servicePath)
		return nil
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "pearl.service").Run()
		userConfig, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		servicePath := filepath.Join(userConfig, "systemd", "user", "pearl.service")
		if err := os.Remove(servicePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("Uninstalled", servicePath)
		return nil
	default:
		return errors.New("service installation is supported on macOS and Linux")
	}
}
