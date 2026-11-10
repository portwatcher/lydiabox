import type { Size } from "../../lib/coords";
import type { OverlayState } from "./overlay";

/**
 * Draws the agent HUD in guest coordinates. The SVG viewBox is the guest
 * frame and `xMidYMid meet` matches the video's `object-fit: contain`, so
 * overlay and video share one geometry at any size.
 */
export function DesktopOverlay({ state, screen }: { state: OverlayState; screen: Size }) {
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
            stroke="var(--color-focus)"
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
              stroke="var(--color-focus)"
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
                stroke="var(--color-focus)"
                strokeWidth={3 * unit}
                style={{ transformBox: "fill-box", transformOrigin: "center" }}
                className="motion-safe:animate-ripple"
              />
            ) : null}
          </g>
        ))}
        {state.cursor ? (
          <g
            style={{ transform: `translate(${state.cursor.x}px, ${state.cursor.y}px) scale(${1.4 * unit})` }}
            className="motion-safe:transition-transform motion-safe:duration-150 motion-safe:ease-out"
          >
            <path
              d="M0 0 L0 22 L6 16.5 L10 25 L13.5 23.5 L9.5 15 L17 15 Z"
              fill="var(--color-accent-solid)"
              stroke="var(--color-on-accent)"
              strokeWidth={1.5}
              strokeLinejoin="round"
            />
          </g>
        ) : null}
      </svg>
      {state.pills.length > 0 ? (
        <div aria-hidden className="pointer-events-none absolute inset-x-0 bottom-6 flex flex-wrap justify-center gap-2 px-4">
          {state.pills.map((p) => (
            <kbd
              key={p.id}
              className="max-w-full truncate rounded-md border border-border bg-surface px-2.5 py-1 font-mono text-sm text-fg shadow-lg motion-safe:animate-fade-in"
            >
              {p.text}
            </kbd>
          ))}
        </div>
      ) : null}
    </>
  );
}
