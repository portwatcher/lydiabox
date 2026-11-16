import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DesktopToolbar } from "./DesktopToolbar";

function render(connected: boolean): string {
  return renderToStaticMarkup(
    createElement(DesktopToolbar, {
      mode: "OBSERVER",
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
    for (const label of ["Observer", "Takeover", "Chrome", "Blender", "Terminal", "2 viewers", "Activity"]) {
      expect(text(html)).toContain(label);
    }
    expect(html).toContain('aria-controls="desktop-feed"');
  });

  it("leaves the VM state and power actions to the status bar", () => {
    for (const html of [render(true), render(false)]) {
      for (const label of ["RUNNING", "STOPPED", "PAUSED", "Hibernate", "Stop", "Wake", "Resume"]) {
        expect(text(html)).not.toContain(label);
      }
    }
  });
});
