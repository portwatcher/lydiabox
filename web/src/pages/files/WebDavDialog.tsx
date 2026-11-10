import type { S3GatewayStatus } from "../../api/types";
import { CopyField } from "../../components/CopyField";
import { Dialog } from "../../components/Dialog";
import { Button } from "../../components/ui";
import { useSystem } from "../../hooks/useSystem";

function Secret({ label }: { label: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[13px] font-medium text-fg">{label}</span>
      <p className="rounded-md border border-border bg-bg px-3 py-2 text-[13px] text-fg-muted">
        Your admin token, the same one you signed in with
      </p>
    </div>
  );
}

function S3Section({ gateway }: { gateway: S3GatewayStatus }) {
  const url = window.location.origin;
  const aws = `AWS_ACCESS_KEY_ID=lydiabox AWS_SECRET_ACCESS_KEY="$LYDIABOX_AUTH_TOKEN" aws --endpoint-url ${url} s3 ls s3://${gateway.bucket}/workspace/`;
  const rclone = `rclone lsd :s3:${gateway.bucket}/workspace --s3-provider=Minio --s3-force-path-style --s3-endpoint=${url} --s3-access-key-id=lydiabox --s3-secret-access-key="$LYDIABOX_AUTH_TOKEN"`;
  return (
    <section className="flex flex-col gap-4 border-t border-border-subtle pt-4">
      <h3 className="text-[13px] font-semibold">S3</h3>
      <div className="grid gap-4 sm:grid-cols-2">
        <CopyField label="Endpoint" value={url} />
        <CopyField label="Bucket" value={gateway.bucket} />
        <CopyField label="Access key" value="lydiabox" />
        <Secret label="Secret key" />
      </div>
      {!gateway.running ? <p className="text-warning-fg">The S3 gateway is not running.</p> : null}
      <p className="text-fg-muted text-pretty">Use path-style addressing.</p>
      <CopyField label="AWS CLI" value={aws} />
      <CopyField label="rclone" value={rclone} />
    </section>
  );
}

/** Endpoints and credentials for mounting the volume over WebDAV (api.md section 5) and S3. */
export function WebDavDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { status } = useSystem();
  const gateway = status?.storage.s3Gateway;
  const endpoint = `${window.location.origin}/webdav/`;
  const rclone = `rclone mount :webdav: ~/lydiabox --webdav-url=${endpoint} --webdav-user=lydiabox --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")"`;
  return (
    <Dialog open={open} onClose={onClose} title="Mount locally" width="lg" footer={<Button onClick={onClose}>Done</Button>}>
      <div className="flex flex-col gap-4 text-sm">
        <h3 className="text-[13px] font-semibold">WebDAV</h3>
        <CopyField label="Endpoint" value={endpoint} />
        <div className="grid gap-4 sm:grid-cols-2">
          <CopyField label="Username" value="lydiabox" />
          <Secret label="Password" />
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
        {gateway?.enabled ? <S3Section gateway={gateway} /> : null}
      </div>
    </Dialog>
  );
}
