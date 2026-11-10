import { CopyField } from "../../components/CopyField";
import { Dialog } from "../../components/Dialog";
import { Button } from "../../components/ui";

/** Endpoint and credentials for mounting the volume over WebDAV (api.md section 5). */
export function WebDavDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const endpoint = `${window.location.origin}/webdav/`;
  const rclone = `rclone mount :webdav: ~/lydiabox --webdav-url=${endpoint} --webdav-user=lydiabox --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")"`;
  return (
    <Dialog open={open} onClose={onClose} title="Mount locally (WebDAV)" width="lg" footer={<Button onClick={onClose}>Done</Button>}>
      <div className="flex flex-col gap-4 text-sm">
        <CopyField label="Endpoint" value={endpoint} />
        <div className="grid gap-4 sm:grid-cols-2">
          <CopyField label="Username" value="lydiabox" />
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-medium text-fg">Password</span>
            <p className="rounded-md border border-border bg-bg px-3 py-2 text-[13px] text-fg-muted">
              Your admin token, the same one you signed in with
            </p>
          </div>
        </div>
        <p className="text-fg-muted text-pretty">
          Your files are in <code className="text-fg">workspace/</code> and <code className="text-fg">home/agent/</code>. They stay
          available while the VM is off.
        </p>
        <div className="flex flex-col gap-1">
          <h3 className="text-[13px] font-semibold">macOS Finder</h3>
          <p className="text-fg-muted text-pretty">Go &gt; Connect to Server, then enter the endpoint.</p>
        </div>
        <CopyField label="Linux (rclone)" value={rclone} />
      </div>
    </Dialog>
  );
}
