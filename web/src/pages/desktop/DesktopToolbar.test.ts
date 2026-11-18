import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DesktopToolbar } from "./DesktopToolbar";

function render(connected: boolean, machine?: string): string {
  return renderToStaticMarkup(
    createElement(DesktopToolbar, {
      machine,
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

  it("shows the VM controls that the page passes, also while the desktop is not connected", () => {
    for (const html of [render(true, "VM-CONTROLS"), render(false, "VM-CONTROLS")]) {
      expect(text(html)).toContain("VM-CONTROLS");
    }
    expect(text(render(true))).not.toContain("Hibernate");
  });
});
