import { AgentCursor } from "../../components/product/AgentCursor";
import type { Size } from "../../lib/coords";
import type { OverlayState } from "./overlay";

/**
 * Draws the agent HUD in guest coordinates. The SVG viewBox is the guest
 * frame and `xMidYMid meet` matches the video's `object-fit: contain`, so
 * overlay and video share one geometry at any size.
 */
export function DesktopOverlay({ state, screen, name = "Agent" }: { state: OverlayState; screen: Size; name?: string }) {
  const unit = screen.width / 1920;
  return (
    <>
      <svg
        aria-hidden
        viewBox={`0 0 ${screen.width} ${screen.height}`}
        preserveAspectRatio="xMidYMid meet"
        className="pointer-events-none absolute inset-0 h-full w-full"
      >
        {state.drag && state.cursor ? (
          <line
            x1={state.drag.x}
            y1={state.drag.y}
            x2={state.cursor.x}
            y2={state.cursor.y}
            stroke="var(--color-info)"
            strokeWidth={3 * unit}
            strokeDasharray={`${10 * unit} ${8 * unit}`}
          />
        ) : null}
        {state.ripples.map((r) => (
          <g key={r.id}>
            <circle
              cx={r.x}
              cy={r.y}
              r={36 * unit}
              fill="none"
              stroke="var(--color-info)"
              strokeWidth={4 * unit}
              style={{ transformBox: "fill-box", transformOrigin: "center" }}
              className="motion-safe:animate-ripple"
            />
            {r.double ? (
              <circle
                cx={r.x}
                cy={r.y}
                r={22 * unit}
                fill="none"
                stroke="var(--color-info)"
                strokeWidth={3 * unit}
                style={{ transformBox: "fill-box", transformOrigin: "center" }}
                className="motion-safe:animate-ripple"
              />
            ) : null}
          </g>
        ))}
        {state.cursor ? <AgentCursor at={state.cursor} trail={state.trail} name={name} unit={unit} /> : null}
      </svg>
      {state.pills.length > 0 ? (
        <div aria-hidden className="pointer-events-none absolute inset-x-0 bottom-6 flex flex-wrap justify-center gap-2 px-4">
          {state.pills.map((p) => (
            <kbd
              key={p.id}
              className="kbd kbd-lg max-w-full truncate shadow-lg motion-safe:animate-fade-in"
            >
              {p.text}
            </kbd>
          ))}
        </div>
      ) : null}
    </>
  );
}
