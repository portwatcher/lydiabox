import { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { EventsClient } from "../../api/events";
import type { SocketState } from "../../api/socket";
import { buildTimeline, chatReducer, emptyChat } from "./chatState";
import type { ChatState, Timeline } from "./chatState";

export interface AgentEvents {
  chat: ChatState;
  timeline: Timeline;
  socket: SocketState;
  /** A stream error that ended it (unknown session). */
  error: string | null;
}

/** Connects to the event stream of a session and keeps its events and status. */
export function useAgentEvents(sessionId: string): AgentEvents {
  const [chat, dispatch] = useReducer(chatReducer, emptyChat);
  const [socket, setSocket] = useState<SocketState>("connecting");
  const [error, setError] = useState<string | null>(null);
  const client = useRef<EventsClient | null>(null);

  useEffect(() => {
    dispatch({ type: "clear" });
    setError(null);
    const c = new EventsClient(sessionId, {
      onEvents: (reset, events) => dispatch({ type: "events", reset, events }),
      onStatus: (status) => dispatch({ type: "status", status }),
      onError: (_code, message) => setError(message),
      onState: setSocket,
    });
    client.current = c;
    c.connect();
    return () => {
      c.close();
      client.current = null;
    };
  }, [sessionId]);

  const timeline = useMemo(() => buildTimeline(chat), [chat]);
  return { chat, timeline, socket, error };
}
