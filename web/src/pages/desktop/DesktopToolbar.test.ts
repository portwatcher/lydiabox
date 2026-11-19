import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DesktopToolbar } from "./DesktopToolbar";

function render(connected: boolean, machine?: string, mode: "OBSERVER" | "TAKEOVER" = "OBSERVER"): string {
  return renderToStaticMarkup(
    createElement(DesktopToolbar, {
      machine,
      activity: { phase: "working", label: "Click at 10, 20", since: 1000, steps: 3 },
      now: 43_000,
      mode,
      modeBusy: false,
      connected,
      viewers: 2,
      sidebar: true,
      onModeChange: () => undefined,
      onLaunch: () => undefined,
      onToggleSidebar: () => undefined,
    }),
  );
}

function text(html: string): string {
  return html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
}

describe("DesktopToolbar", () => {
  it("keeps the desktop controls", () => {
    const html = render(true);
    for (const label of ["Take over", "Agent has control", "Chrome", "Blender", "Terminal", "2 viewers", "Click at 10, 20", "42s · 3 steps"]) {
      expect(text(html)).toContain(label);
    }
    expect(html).toContain('aria-controls="desktop-feed"');
    expect(html).toContain('aria-expanded="true"');
  });

  it("offers to hand control back during a takeover", () => {
    const html = text(render(true, undefined, "TAKEOVER"));
    expect(html).toContain("You have control");
    expect(html).toContain("Hand back to the agent");
  });

  it("disables the handoff while the desktop is not connected", () => {
    expect(render(false)).toMatch(/<button[^>]*disabled=""[^>]*>.*Take over/);
  });

  it("shows the VM controls that the page passes, also while the desktop is not connected", () => {
    for (const html of [render(true, "VM-CONTROLS"), render(false, "VM-CONTROLS")]) {
      expect(text(html)).toContain("VM-CONTROLS");
    }
    expect(text(render(true))).not.toContain("Hibernate");
  });
});
