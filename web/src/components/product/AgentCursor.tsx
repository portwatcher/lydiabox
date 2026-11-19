export interface CursorPoint {
  x: number;
  y: number;
}

export interface AgentCursorProps {
  /** Cursor position in the SVG's user units. */
  at: CursorPoint;
  /** Earlier points, oldest first; drawn as a fading trail. */
  trail: CursorPoint[];
  /** The name on the tag beside the cursor. */
  name: string;
  /** Size of one design pixel in user units (the SVG may be any scale). */
  unit: number;
}

/** Approximate width of the tag text, in design pixels at 13 px. */
export function tagWidth(name: string): number {
  return Math.round(name.length * 7.4 + 16);
}

/**
 * A named agent cursor for an SVG overlay: an arrow with a name tag and a
 * trail of its recent path. It glides to new points with a CSS transition
 * that reduced motion turns off.
 */
export function AgentCursor({ at, trail, name, unit }: AgentCursorProps) {
  const points = [...trail, at];
  const width = tagWidth(name);
  return (
    <g>
      {points.slice(1).map((p, i) => {
        const from = points[i];
        // Older segments are fainter and thinner.
        const age = (i + 1) / points.length;
        return (
          <line
            key={`${i}-${from.x}-${from.y}`}
            x1={from.x}
            y1={from.y}
            x2={p.x}
            y2={p.y}
            stroke="var(--color-info)"
            strokeOpacity={0.15 + 0.5 * age}
            strokeWidth={(1.5 + 2.5 * age) * unit}
            strokeLinecap="round"
          />
        );
      })}
      <g
        style={{ transform: `translate(${at.x}px, ${at.y}px) scale(${1.4 * unit})` }}
        className="motion-safe:transition-transform motion-safe:duration-200 motion-safe:ease-out"
      >
        <path
          d="M0 0 L0 22 L6 16.5 L10 25 L13.5 23.5 L9.5 15 L17 15 Z"
          fill="var(--color-info)"
          stroke="var(--color-base-300)"
          strokeWidth={1.5}
          strokeLinejoin="round"
        />
        <g transform="translate(14 22)">
          <rect width={width} height={20} rx={10} fill="var(--color-info)" stroke="var(--color-base-300)" strokeWidth={1} />
          <text x={width / 2} y={14} textAnchor="middle" fontSize={12} fontWeight={600} fill="var(--color-info-content)" fontFamily="var(--font-sans)">
            {name}
          </text>
        </g>
      </g>
    </g>
  );
}
