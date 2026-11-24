package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/portwatcher/lydiabox/internal/acp"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
)

// acp runs the ACP agent of a harness for the host: LydiaBox starts it in a
// guest terminal and exchanges JSON lines with it (internal/acp).
//
//	lydiabox-guest acp --harness codex --cwd /workspace/app [--resume ID] [--model M] [--mcp name=url]...
func init() {
	Register(Command{Name: "acp", Summary: "Run the ACP agent of a harness for the LydiaBox chat view", Run: runACP})
}

type mcpFlags []acp.MCPServer

func (m *mcpFlags) String() string { return fmt.Sprint(*m) }

func (m *mcpFlags) Set(v string) error {
	name, url, ok := strings.Cut(v, "=")
	if !ok || name == "" || url == "" {
		return errors.New("want name=url")
	}
	*m = append(*m, acp.MCPServer{Name: name, URL: url})
	return nil
}

// tail keeps the end of the agent's error output for the error message.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - 4096; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func runACP(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	h := fs.String("harness", "", "harness ID")
	cwd := fs.String("cwd", "", "working directory of the session")
	resume := fs.String("resume", "", "native session ID to continue")
	model := fs.String("model", "", "model ID")
	var mcp mcpFlags
	fs.Var(&mcp, "mcp", "MCP server name=url (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agent, err := acp.AgentFor(*h, *model)
	if err != nil {
		return err
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	if f, ok := env.Stdin.(*os.File); ok {
		defer acp.MakeRaw(f)()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	cmd := exec.Command(agent.Command[0], agent.Command[1:]...)
	cmd.Dir = *cwd
	cmd.Env = os.Environ()
	for k, v := range agent.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	errs := &tail{}
	cmd.Stderr = errs
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", agent.Command[0], err)
	}
	home, _ := os.UserHomeDir()
	b := acp.NewBridge(acp.Options{Harness: *h, Cwd: *cwd, Resume: *resume, Model: *model, MCP: mcp, Home: home,
		History: adapters.NewEventReader(), In: env.Stdin, Out: env.Stdout,
		Agent: struct {
			io.Reader
			io.Writer
		}{out, in}})
	runErr := b.Run(ctx)
	_ = in.Close()
	end(cmd)
	if runErr != nil {
		if t := errs.String(); t != "" {
			return fmt.Errorf("%w\n%s", runErr, t)
		}
	}
	return runErr
}

// end stops the agent process group: SIGTERM, then SIGKILL after 3 s.
func end(cmd *exec.Cmd) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}
