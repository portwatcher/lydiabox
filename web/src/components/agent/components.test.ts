import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { AgentStatus } from "./AgentStatus";
import type { ToolItem } from "./chatState";
import { CodeDiff } from "./CodeDiff";
import { ContextMeter } from "./ContextMeter";
import { GenerationStats } from "./GenerationStats";
import { TerminalHandoff, DesktopHandoff } from "./HandoffBanner";
import { Markdown, parseBlocks, safeHref } from "./Markdown";
import { PlanCard } from "./PlanCard";
import { splitSource } from "./SourceCards";
import { transcriptOf, recognitionCtor } from "./speech";
import { StarterPrompts, starterPrompts } from "./StarterPrompts";
import { ToolCallCard, ToolGroup } from "./ToolCallCard";
import { diffCounts, formatDuration, toolLabel } from "./toolKinds";

const html = (el: Parameters<typeof renderToStaticMarkup>[0]) => renderToStaticMarkup(el);

const item = (over: Partial<ToolItem> = {}): ToolItem => ({
  type: "tool",
  key: "tool:c1",
  time: "",
  call: { id: "c1", name: "Bash", kind: "shell", summary: "go test ./...", input: '{"command":"go test ./..."}', path: "", diff: "" },
  ...over,
});

describe("tool call card", () => {
  it("shows the verb, target, duration, and state in words", () => {
    const out = html(createElement(ToolCallCard, { item: item({ result: { callId: "c1", status: "error", output: "FAIL", images: [], durationMs: 1200 } }), active: false }));
    expect(out).toContain("Ran");
    expect(out).toContain("go test ./...");
    expect(out).toContain("1.2s");
    expect(out).toContain("Failed");
    expect(out).toContain("Error");
    expect(out).toContain("<details");
  });

  it("shows a running call while the turn works and a screenshot as a thumbnail", () => {
    const running = html(createElement(ToolCallCard, { item: item(), active: true }));
    expect(running).toContain("Running");
    const shot = item({
      call: { id: "s", name: "mcp__lydiabox__computer_screenshot", kind: "computer", summary: "", input: "", path: "", diff: "" },
      result: { callId: "s", status: "ok", output: "", images: ["data:image/png;base64,AA"], durationMs: 0 },
    });
    const out = html(createElement(ToolCallCard, { item: shot, active: false }));
    expect(out).toContain('src="data:image/png;base64,AA"');
    expect(out).toContain('alt="Screenshot from the tool"');
  });

  it("folds three or more calls into one row", () => {
    const tools = [item({ key: "a" }), item({ key: "b" }), item({ key: "c", result: { callId: "c1", status: "error", output: "", images: [], durationMs: 0 } })];
    const out = html(createElement(ToolGroup, { tools, active: false }));
    expect(out).toContain("Used 3 tools");
    expect(out).toContain("1 failed");
    expect(html(createElement(ToolGroup, { tools, active: true }))).toContain("Using 3 tools");
  });
});

describe("diff, plan, context, and stats", () => {
  it("counts and tints diff lines", () => {
    const out = html(createElement(CodeDiff, { diff: " ctx\n-old\n+new\n+more", path: "a.go" }));
    expect(out).toContain("+2");
    expect(out).toContain("−1");
    expect(out).toContain("bg-success/10");
    expect(diffCounts("--- a\n+++ b\n-x\n+y")).toEqual({ added: 1, removed: 1 });
  });

  it("shows plan progress and the state of each step in words", () => {
    const out = html(createElement(PlanCard, { steps: [{ text: "A", status: "completed" }, { text: "B", status: "in_progress" }, { text: "C", status: "pending" }] }));
    expect(out).toContain("1 of 3");
    expect(out).toContain("(In progress)");
    expect(out).toContain("(To do)");
  });

  it("warns near the end of the context window", () => {
    expect(html(createElement(ContextMeter, { tokens: 850, window: 1000 }))).toContain("text-warning");
    expect(html(createElement(ContextMeter, { tokens: 990, window: 1000 }))).toContain("text-error");
    const calm = html(createElement(ContextMeter, { tokens: 100, window: 1000 }));
    expect(calm).toContain('aria-valuenow="10"');
    expect(html(createElement(ContextMeter, { tokens: 5000, window: 0 }))).toContain("tokens");
    expect(html(createElement(ContextMeter, { tokens: 0, window: 10 }))).toBe("");
  });

  it("lists the totals of a turn", () => {
    const out = html(createElement(GenerationStats, { stats: { durationMs: 3640, inputTokens: 1200, outputTokens: 300, costUsd: 0.05, model: "kimi/k3", tools: 2 }, interrupted: false }));
    expect(out).toContain("Done in");
    expect(out).toContain("3.6s");
    expect(out).toContain("kimi/k3");
    expect(html(createElement(GenerationStats, { stats: { durationMs: 0, inputTokens: 0, outputTokens: 0, costUsd: 0, model: "", tools: 0 }, interrupted: true }))).toContain("Stopped");
  });
});

describe("status, handoff, and starters", () => {
  it("names every state in words", () => {
    expect(html(createElement(AgentStatus, { state: "attention" }))).toContain("Needs your input");
    expect(html(createElement(AgentStatus, { state: "offline" }))).toContain("Not running");
    expect(html(createElement(AgentStatus, { state: "working", since: Date.now() - 5000 }))).toMatch(/Working.*\ds/);
  });

  it("offers the terminal for an approval and the desktop for computer use", () => {
    const t = html(createElement(TerminalHandoff, { prompt: "Do you want to proceed?", onOpenTerminal: () => undefined }));
    expect(t).toContain("Open terminal");
    expect(t).toContain("Do you want to proceed?");
    const d = html(createElement(MemoryRouter, null, createElement(DesktopHandoff)));
    expect(d).toContain('href="/desktop"');
  });

  it("lists starter prompts as buttons", () => {
    const out = html(createElement(StarterPrompts, { onPick: () => undefined }));
    for (const p of starterPrompts) {
      expect(out).toContain(p.title);
    }
    expect(out.match(/<button/g)).toHaveLength(starterPrompts.length);
  });
});

describe("markdown", () => {
  it("renders blocks and keeps only safe links", () => {
    expect(parseBlocks("# T\n\ntext\n- a\n- b\n\n```go\nx := 1\n```").map((b) => b.type)).toEqual(["heading", "para", "list", "code"]);
    const out = html(createElement(Markdown, { text: "Use `go test` and **care**. [ok](https://example.com) [bad](javascript:alert(1))" }));
    expect(out).toContain("<code");
    expect(out).toContain("<strong>care</strong>");
    expect(out).toContain('href="https://example.com"');
    expect(out).not.toContain("href=\"javascript");
    expect(safeHref("mailto:a@b.c")).toBe("mailto:a@b.c");
    expect(safeHref("data:text/html,x")).toBeNull();
    expect(html(createElement(Markdown, { text: "<img src=x onerror=alert(1)>" }))).toContain("&lt;img");
  });
});

describe("helpers", () => {
  it("formats durations, tool labels, and sources", () => {
    expect(formatDuration(850)).toBe("850ms");
    expect(formatDuration(12_400)).toBe("12s");
    expect(formatDuration(3_640)).toBe("3.6s");
    expect(formatDuration(95_000)).toBe("1m 35s");
    expect(formatDuration(0)).toBe("");
    expect(toolLabel("mcp__github__create_issue")).toBe("github create_issue");
    expect(toolLabel("Bash")).toBe("Bash");
    expect(splitSource("/workspace/app/main.go")).toEqual(["main.go", "/workspace/app"]);
    expect(splitSource("https://go.dev/doc/")).toEqual(["go.dev", "/doc/"]);
  });

  it("reads dictation results", () => {
    const results = [
      Object.assign([{ transcript: "hello " }], { isFinal: true }),
      Object.assign([{ transcript: "wor" }], { isFinal: false }),
    ];
    expect(transcriptOf({ resultIndex: 0, results })).toEqual({ final: "hello ", interim: "wor" });
    expect(recognitionCtor({})).toBeNull();
    function Fake() {
      return undefined;
    }
    expect(recognitionCtor({ webkitSpeechRecognition: Fake })).toBe(Fake);
  });
});
