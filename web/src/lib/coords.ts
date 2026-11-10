// Maps between the on-screen video box and the guest frame (1920 x 1080 by
// default). The video uses `object-fit: contain`, so the frame is centered
// with letterbox or pillarbox bars; the SVG overlay uses the same geometry
// through `viewBox` + `preserveAspectRatio="xMidYMid meet"`.

export interface Size {
  width: number;
  height: number;
}

export interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** The rectangle the frame occupies inside `container` (client pixels). */
export function contentBox(container: Box, frame: Size): Box {
  if (container.width <= 0 || container.height <= 0 || frame.width <= 0 || frame.height <= 0) {
    return { left: container.left, top: container.top, width: 0, height: 0 };
  }
  const scale = Math.min(container.width / frame.width, container.height / frame.height);
  const width = frame.width * scale;
  const height = frame.height * scale;
  return {
    left: container.left + (container.width - width) / 2,
    top: container.top + (container.height - height) / 2,
    width,
    height,
  };
}

/**
 * Converts a client point to guest coordinates, clamped to the frame.
 * Returns null when the point is outside the visible frame (in the bars).
 */
export function toGuest(clientX: number, clientY: number, container: Box, frame: Size): { x: number; y: number } | null {
  const box = contentBox(container, frame);
  if (box.width === 0 || box.height === 0) {
    return null;
  }
  const rx = (clientX - box.left) / box.width;
  const ry = (clientY - box.top) / box.height;
  if (rx < 0 || rx > 1 || ry < 0 || ry > 1) {
    return null;
  }
  return {
    x: Math.min(frame.width - 1, Math.max(0, Math.round(rx * frame.width))),
    y: Math.min(frame.height - 1, Math.max(0, Math.round(ry * frame.height))),
  };
}

/** Wheel deltas in pixels; line and page modes are scaled to pixel equivalents. */
export function wheelPixels(deltaX: number, deltaY: number, deltaMode: number, pageHeight: number): { dx: number; dy: number } {
  const factor = deltaMode === 1 ? 40 : deltaMode === 2 ? pageHeight : 1;
  return { dx: Math.round(deltaX * factor), dy: Math.round(deltaY * factor) };
}

/** DOM `MouseEvent.button` to the data channel button name. */
export function buttonName(button: number): "left" | "middle" | "right" | null {
  switch (button) {
    case 0:
      return "left";
    case 1:
      return "middle";
    case 2:
      return "right";
    default:
      return null;
  }
}
