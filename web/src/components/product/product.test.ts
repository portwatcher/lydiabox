import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { gatewayHealth, healthSummary, providerHealth } from "./health";
import { HealthLabel, Mark, initials } from "./Marks";
import { ModelCard } from "./ModelCard";
import { CommandBlock, SnippetSteps } from "./Snippet";
import { Meter, StatCells } from "./StatCells";

describe("health", () => {
  it("maps the magpie provider status", () => {
    expect(providerHealth({ enabled: true, status: "ok" })).toBe("operational");
    expect(providerHealth({ enabled: true, status: "pending" })).toBe("pending");
    expect(providerHealth({ enabled: true, status: "error", lastError: "401" })).toBe("outage");
    expect(providerHealth({ enabled: true, status: "disabled" })).toBe("off");
    expect(providerHealth({ enabled: false, status: "ok" })).toBe("off");
    expect(providerHealth({ enabled: true, status: "", lastError: "boom" })).toBe("outage");
    expect(providerHealth({ enabled: true, status: "" })).toBe("unknown");
  });

  it("maps the gateway state", () => {
    expect(gatewayHealth("running")).toBe("operational");
    expect(gatewayHealth("starting")).toBe("pending");
    expect(gatewayHealth("error")).toBe("outage");
    expect(gatewayHealth("stopped")).toBe("off");
    expect(gatewayHealth(null)).toBe("unknown");
  });

  it("summarizes worst first and skips absent states", () => {
    expect(healthSummary(["operational", "outage", "operational", "off"])).toBe("1 with errors · 2 operational · 1 off");
    expect(healthSummary([])).toBe("");
  });
});

describe("marks", () => {
  it("takes one or two initials", () => {
    expect(initials("Google Drive")).toBe("GD");
    expect(initials("openai")).toBe("O");
    expect(initials("me@example.com")).toBe("ME");
    expect(initials("  ")).toBe("?");
  });

  it("hides the mark from screen readers and labels health in text", () => {
    expect(renderToStaticMarkup(createElement(Mark, { name: "Notion" }))).toContain('aria-hidden="true"');
    const html = renderToStaticMarkup(createElement(HealthLabel, { health: "outage" }));
    expect(html).toContain("Error");
    expect(html).toContain("text-error");
  });
});

describe("snippet", () => {
  it("copies commands without comment lines and prompts only commands", () => {
    const html = renderToStaticMarkup(createElement(CommandBlock, { code: "# set up\nlydiabox up" }));
    expect(html).toContain("# set up");
    expect(html).toContain("lydiabox up");
    expect(html.match(/\$ /g)?.length).toBe(1);
    expect(html).toContain('aria-label="Copy command"');
  });

  it("numbers steps and checks finished ones", () => {
    const html = renderToStaticMarkup(
      createElement(SnippetSteps, {
        steps: [
          { title: "Install", done: true, children: "a" },
          { title: "Run", children: "b" },
        ],
      }),
    );
    expect(html).toContain('aria-label="Done"');
    expect(html).toContain(">2<");
    expect(html).not.toContain(">1<");
  });
});

describe("stats", () => {
  it("renders labeled cells", () => {
    const html = renderToStaticMarkup(createElement(StatCells, { cells: [{ label: "Cost", value: "$1.00" }] }));
    expect(html).toContain("<dt");
    expect(html).toContain("$1.00");
  });

  it("scales the meter and clamps it", () => {
    expect(renderToStaticMarkup(createElement(Meter, { value: 50, max: 100, label: "x" }))).toContain("width:50%");
    expect(renderToStaticMarkup(createElement(Meter, { value: 500, max: 100, label: "x" }))).toContain("width:100%");
    expect(renderToStaticMarkup(createElement(Meter, { value: 0, max: 0, label: "x" }))).toContain("width:0%");
    const log = renderToStaticMarkup(createElement(Meter, { value: 8000, max: 2_000_000, log: true, label: "x" }));
    expect(log).toMatch(/width:6\d%/);
  });

  it("shows bindings and groups on a model card", () => {
    const html = renderToStaticMarkup(
      createElement(ModelCard, { id: "openai/gpt-5.2", name: "gpt-5.2", contextWindow: 400_000, maxContext: 2_000_000, boundTo: ["Codex"], groups: ["budget"] }),
    );
    expect(html).toContain("openai/gpt-5.2");
    expect(html).toContain("Codex");
    expect(html).toContain("budget");
    expect(html).toContain('role="meter"');
  });
});
