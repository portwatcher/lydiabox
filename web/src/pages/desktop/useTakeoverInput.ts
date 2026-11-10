import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import type { InputMessage, MouseButtonName } from "../../api/types";
import { buttonName, toGuest, wheelPixels } from "../../lib/coords";
import type { Size } from "../../lib/coords";

/** Shift+Escape gives the keyboard back to the page (focus leaves the stage). */
export function isReleaseShortcut(event: Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "altKey" | "metaKey">): boolean {
  return event.key === "Escape" && event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey;
}

/**
 * Captures pointer and keyboard input on `stageRef` while `enabled` and sends
 * it over the data channel in guest coordinates. Moves are coalesced to one
 * per animation frame. Pressed keys and buttons are released when capture
 * stops or the stage loses focus, so nothing stays stuck down in the guest.
 */
export function useTakeoverInput(
  stageRef: RefObject<HTMLElement | null>,
  enabled: boolean,
  screen: Size,
  send: (message: InputMessage) => void,
  onRelease: () => void,
): void {
  const sendRef = useRef(send);
  const releaseRef = useRef(onRelease);
  useEffect(() => {
    sendRef.current = send;
    releaseRef.current = onRelease;
  });

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || !enabled) {
      return;
    }
    const keys = new Map<string, string>();
    const buttons = new Set<MouseButtonName>();
    let frame = 0;
    let pendingMove: { x: number; y: number } | null = null;
    let composing = false;

    const emit = (message: InputMessage) => sendRef.current(message);
    const point = (event: MouseEvent) => toGuest(event.clientX, event.clientY, stage.getBoundingClientRect(), screen);

    const flushMove = () => {
      frame = 0;
      if (pendingMove) {
        emit({ t: "move", ...pendingMove });
        pendingMove = null;
      }
    };

    const releaseAll = () => {
      for (const [code, key] of keys) {
        emit({ t: "keyup", code, key });
      }
      keys.clear();
      for (const button of buttons) {
        emit({ t: "up", button });
      }
      buttons.clear();
    };

    const onPointerMove = (event: PointerEvent) => {
      const p = point(event);
      if (!p) {
        return;
      }
      pendingMove = p;
      if (!frame) {
        frame = requestAnimationFrame(flushMove);
      }
    };

    const onPointerDown = (event: PointerEvent) => {
      const button = buttonName(event.button);
      const p = point(event);
      if (!button || !p) {
        return;
      }
      event.preventDefault();
      stage.focus({ preventScroll: true });
      stage.setPointerCapture(event.pointerId);
      pendingMove = null;
      emit({ t: "move", ...p });
      emit({ t: "down", button });
      buttons.add(button);
    };

    const onPointerUp = (event: PointerEvent) => {
      const button = buttonName(event.button);
      if (!button || !buttons.has(button)) {
        return;
      }
      event.preventDefault();
      const p = point(event);
      if (p) {
        emit({ t: "move", ...p });
      }
      emit({ t: "up", button });
      buttons.delete(button);
    };

    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const { dx, dy } = wheelPixels(event.deltaX, event.deltaY, event.deltaMode, stage.clientHeight);
      if (dx !== 0 || dy !== 0) {
        emit({ t: "wheel", dx, dy });
      }
    };

    const onKeyDown = (event: KeyboardEvent) => {
      if (isReleaseShortcut(event)) {
        event.preventDefault();
        releaseAll();
        stage.blur();
        releaseRef.current();
        return;
      }
      if (composing || event.isComposing) {
        return;
      }
      event.preventDefault();
      keys.set(event.code, event.key);
      emit({ t: "keydown", code: event.code, key: event.key });
    };

    const onKeyUp = (event: KeyboardEvent) => {
      if (composing || event.isComposing) {
        return;
      }
      event.preventDefault();
      keys.delete(event.code);
      emit({ t: "keyup", code: event.code, key: event.key });
    };

    const onCompositionStart = () => {
      composing = true;
    };
    const onCompositionEnd = (event: CompositionEvent) => {
      composing = false;
      if (event.data) {
        emit({ t: "text", text: event.data });
      }
    };
    const onPaste = (event: ClipboardEvent) => {
      const text = event.clipboardData?.getData("text/plain");
      if (text) {
        event.preventDefault();
        emit({ t: "text", text });
      }
    };
    const onContextMenu = (event: Event) => event.preventDefault();

    stage.addEventListener("pointermove", onPointerMove);
    stage.addEventListener("pointerdown", onPointerDown);
    stage.addEventListener("pointerup", onPointerUp);
    stage.addEventListener("wheel", onWheel, { passive: false });
    stage.addEventListener("keydown", onKeyDown);
    stage.addEventListener("keyup", onKeyUp);
    stage.addEventListener("compositionstart", onCompositionStart);
    stage.addEventListener("compositionend", onCompositionEnd);
    stage.addEventListener("paste", onPaste);
    stage.addEventListener("contextmenu", onContextMenu);
    stage.addEventListener("blur", releaseAll);

    return () => {
      if (frame) {
        cancelAnimationFrame(frame);
      }
      releaseAll();
      stage.removeEventListener("pointermove", onPointerMove);
      stage.removeEventListener("pointerdown", onPointerDown);
      stage.removeEventListener("pointerup", onPointerUp);
      stage.removeEventListener("wheel", onWheel);
      stage.removeEventListener("keydown", onKeyDown);
      stage.removeEventListener("keyup", onKeyUp);
      stage.removeEventListener("compositionstart", onCompositionStart);
      stage.removeEventListener("compositionend", onCompositionEnd);
      stage.removeEventListener("paste", onPaste);
      stage.removeEventListener("contextmenu", onContextMenu);
      stage.removeEventListener("blur", releaseAll);
    };
  }, [stageRef, enabled, screen]);
}
