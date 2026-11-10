import { useCallback, useEffect, useRef, useState } from "react";
import { DesktopSignalClient } from "../../api/desktopSignal";
import type { SocketState } from "../../api/socket";
import type { AgentAction, DesktopMode, DesktopServerMessage, IceCandidate, InputMessage, LaunchApp } from "../../api/types";

export interface DesktopStream {
  /** Signaling socket state; null until connect() is called. */
  socket: SocketState | null;
  peer: RTCPeerConnectionState | "new";
  stream: MediaStream | null;
  mode: DesktopMode;
  viewers: number;
  screen: { width: number; height: number };
  error: string | null;
  inputReady: boolean;
  connect: () => void;
  disconnect: () => void;
  retry: () => void;
  sendInput: (message: InputMessage) => void;
  launch: (app: LaunchApp) => boolean;
  /** Applies a mode change reported by the REST route. */
  setMode: (mode: DesktopMode) => void;
}

function toCandidate(init: RTCIceCandidateInit): IceCandidate {
  return {
    candidate: init.candidate ?? "",
    sdpMid: init.sdpMid ?? null,
    sdpMLineIndex: init.sdpMLineIndex ?? null,
    usernameFragment: init.usernameFragment ?? null,
  };
}

/** Prefers H.264, which the guest encodes (`ffmpeg ... libx264`). */
function preferH264(transceiver: RTCRtpTransceiver): void {
  if (typeof RTCRtpReceiver === "undefined" || !RTCRtpReceiver.getCapabilities || !transceiver.setCodecPreferences) {
    return;
  }
  const caps = RTCRtpReceiver.getCapabilities("video");
  const h264 = caps?.codecs.filter((c) => c.mimeType.toLowerCase() === "video/h264") ?? [];
  if (h264.length > 0) {
    try {
      transceiver.setCodecPreferences(h264);
    } catch {
      // Keep the browser's default order when the preference is rejected.
    }
  }
}

/**
 * Owns the signaling socket, the RTCPeerConnection (one recvonly H.264 video
 * transceiver plus the `input` data channel), and their reconnects. Each
 * `hello` (first connect or reconnect) builds a fresh peer connection.
 */
export function useDesktopStream(onAgentAction: (action: AgentAction) => void): DesktopStream {
  const [socket, setSocket] = useState<SocketState | null>(null);
  const [peer, setPeer] = useState<RTCPeerConnectionState | "new">("new");
  const [stream, setStream] = useState<MediaStream | null>(null);
  const [mode, setMode] = useState<DesktopMode>("OBSERVER");
  const [viewers, setViewers] = useState(0);
  const [screen, setScreen] = useState({ width: 1920, height: 1080 });
  const [error, setError] = useState<string | null>(null);
  const [inputReady, setInputReady] = useState(false);

  const clientRef = useRef<DesktopSignalClient | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const channelRef = useRef<RTCDataChannel | null>(null);
  const pendingIce = useRef<IceCandidate[]>([]);
  const actionRef = useRef(onAgentAction);
  useEffect(() => {
    actionRef.current = onAgentAction;
  });

  const closePeer = useCallback(() => {
    channelRef.current?.close();
    channelRef.current = null;
    pcRef.current?.close();
    pcRef.current = null;
    pendingIce.current = [];
    setInputReady(false);
    setStream(null);
    setPeer("new");
  }, []);

  const startPeer = useCallback(
    async (client: DesktopSignalClient, iceServers: RTCIceServer[]) => {
      closePeer();
      const pc = new RTCPeerConnection({ iceServers });
      pcRef.current = pc;
      const transceiver = pc.addTransceiver("video", { direction: "recvonly" });
      preferH264(transceiver);
      const channel = pc.createDataChannel("input", { ordered: true });
      channelRef.current = channel;
      channel.onopen = () => setInputReady(true);
      channel.onclose = () => setInputReady(false);
      pc.ontrack = (event) => {
        setStream(event.streams[0] ?? new MediaStream([event.track]));
      };
      pc.onicecandidate = (event) => {
        if (event.candidate) {
          client.send({ type: "ice", candidate: toCandidate(event.candidate.toJSON()) });
        }
      };
      pc.onconnectionstatechange = () => {
        if (pcRef.current === pc) {
          setPeer(pc.connectionState);
        }
      };
      try {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        if (pcRef.current === pc && offer.sdp) {
          client.send({ type: "offer", sdp: offer.sdp });
        }
      } catch (err) {
        setError(`Video stream not started. ${err instanceof Error ? err.message : ""}`.trim());
      }
    },
    [closePeer],
  );

  const handle = useCallback(
    (client: DesktopSignalClient, msg: DesktopServerMessage) => {
      switch (msg.type) {
        case "hello":
          setError(null);
          setMode(msg.mode);
          setScreen(msg.screen);
          void startPeer(
            client,
            msg.iceServers.map((s) => ({ urls: s.urls, username: s.username, credential: s.credential })),
          );
          return;
        case "answer": {
          const pc = pcRef.current;
          if (!pc) {
            return;
          }
          pc.setRemoteDescription({ type: "answer", sdp: msg.sdp })
            .then(async () => {
              const queued = pendingIce.current;
              pendingIce.current = [];
              for (const candidate of queued) {
                await pc.addIceCandidate(candidate).catch(() => undefined);
              }
            })
            .catch((err: unknown) => setError(`Video stream not started. ${err instanceof Error ? err.message : ""}`.trim()));
          return;
        }
        case "ice": {
          const pc = pcRef.current;
          if (!pc) {
            return;
          }
          if (!pc.remoteDescription) {
            pendingIce.current.push(msg.candidate);
            return;
          }
          void pc.addIceCandidate(msg.candidate).catch(() => undefined);
          return;
        }
        case "mode":
          setMode(msg.mode);
          return;
        case "viewers":
          setViewers(msg.count);
          return;
        case "agent_action":
          actionRef.current(msg);
          return;
        case "error":
          setError(msg.message || msg.code);
          return;
        case "pong":
          return;
      }
    },
    [startPeer],
  );

  const disconnect = useCallback(() => {
    clientRef.current?.close();
    clientRef.current = null;
    closePeer();
    setSocket(null);
    setViewers(0);
  }, [closePeer]);

  const connect = useCallback(() => {
    if (clientRef.current) {
      return;
    }
    setError(null);
    const client: DesktopSignalClient = new DesktopSignalClient({
      onMessage: (msg) => handle(client, msg),
      onState: (state) => {
        setSocket(state);
        if (state !== "open") {
          closePeer();
        }
      },
    });
    clientRef.current = client;
    client.connect();
  }, [handle, closePeer]);

  const retry = useCallback(() => {
    clientRef.current?.retry();
  }, []);

  useEffect(() => disconnect, [disconnect]);

  const sendInput = useCallback((message: InputMessage) => {
    const channel = channelRef.current;
    if (channel && channel.readyState === "open") {
      channel.send(JSON.stringify(message));
    }
  }, []);

  const launch = useCallback((app: LaunchApp) => clientRef.current?.send({ type: "launch", app }) ?? false, []);

  return { socket, peer, stream, mode, viewers, screen, error, inputReady, connect, disconnect, retry, sendInput, launch, setMode };
}
