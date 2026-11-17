import { CircleAlert, LogIn, Plus, Trash2 } from "lucide-react";
import { useCallback, useState } from "react";
import { describeError } from "../../api/errors";
import { models } from "../../api/domains";
import type { Subscription, SubscriptionKind } from "../../api/types";
import { ConfirmDialog } from "../../components/Dialog";
import { useToast } from "../../components/Toast";
import { Badge, Button, IconButton, Panel } from "../../components/ui";
import { count } from "../../lib/format";
import { SignInDialog } from "./SignInDialog";

interface SubscriptionCardsProps {
  subscriptions: Subscription[];
  /** Reloads subscriptions, providers, and models after a change. */
  onChanged: () => void;
}

interface Removal {
  kind: SubscriptionKind;
  user: string;
}

function SubscriptionCard({ sub, onSignIn, onRemove, onToggle, toggling }: { sub: Subscription; onSignIn: () => void; onRemove: (user: string) => void; onToggle: () => void; toggling: boolean }) {
  const switchId = `sub-enabled-${sub.kind}`;
  const signedIn = sub.accounts.length > 0;
  return (
    <li className="flex flex-col gap-3 rounded-lg border border-border-subtle bg-surface p-4">
      <div className="flex items-center gap-2">
        <h3 className="min-w-0 flex-1 truncate text-sm font-semibold">{sub.name}</h3>
        {signedIn ? <Badge>{count(sub.models.length, { one: "# model", other: "# models" })}</Badge> : null}
      </div>
      {signedIn ? (
        <ul className="flex flex-col divide-y divide-border-subtle">
          {sub.accounts.map((a) => (
            <li key={a.user} className="flex items-center gap-2 py-1.5">
              <span className="min-w-0 flex-1 truncate text-[13px]" title={a.user}>
                {a.user}
              </span>
              {a.needsSignIn ? (
                <Badge tone="warning" icon={CircleAlert}>
                  Sign in again
                </Badge>
              ) : a.plan ? (
                <Badge>{a.plan}</Badge>
              ) : null}
              <IconButton icon={Trash2} tone="danger" label={`Remove ${a.user}`} onClick={() => onRemove(a.user)} />
            </li>
          ))}
        </ul>
      ) : null}
      <div className="mt-auto flex flex-wrap items-center gap-3">
        {signedIn ? (
          <>
            <label htmlFor={switchId} className="flex flex-1 items-center gap-2 text-[13px] text-fg">
              <input
                id={switchId}
                type="checkbox"
                role="switch"
                checked={sub.enabled}
                disabled={toggling}
                onChange={onToggle}
                className="size-4 accent-accent-solid"
              />
              Route requests to this subscription
            </label>
            <Button size="sm" icon={Plus} onClick={onSignIn}>
              Add account
            </Button>
          </>
        ) : (
          <Button size="sm" variant="primary" icon={LogIn} onClick={onSignIn}>
            Sign in
          </Button>
        )}
      </div>
    </li>
  );
}

/** Claude and ChatGPT subscriptions that magpie signs in on the host. */
export function SubscriptionCards({ subscriptions, onChanged }: SubscriptionCardsProps) {
  const toast = useToast();
  const [signingIn, setSigningIn] = useState<Subscription | null>(null);
  const [removing, setRemoving] = useState<Removal | null>(null);
  const [toggling, setToggling] = useState<SubscriptionKind | null>(null);

  const closeSignIn = useCallback(() => setSigningIn(null), []);
  const signedIn = useCallback(
    (account: string) => {
      setSigningIn(null);
      onChanged();
      toast.success(account ? `${account} signed in` : "Account signed in");
    },
    [onChanged, toast],
  );

  const toggle = async (sub: Subscription) => {
    setToggling(sub.kind);
    try {
      await models.updateProvider(sub.providerId, { enabled: !sub.enabled });
      onChanged();
    } catch (err) {
      toast.error(describeError(err, `${sub.name} not updated`));
    } finally {
      setToggling(null);
    }
  };

  return (
    <Panel title="Subscriptions">
      <ul className="grid gap-3 p-4 sm:grid-cols-2">
        {subscriptions.map((s) => (
          <SubscriptionCard
            key={s.kind}
            sub={s}
            toggling={toggling === s.kind}
            onSignIn={() => setSigningIn(s)}
            onRemove={(user) => setRemoving({ kind: s.kind, user })}
            onToggle={() => void toggle(s)}
          />
        ))}
      </ul>
      <SignInDialog kind={signingIn?.kind ?? null} name={signingIn?.name ?? ""} onClose={closeSignIn} onSignedIn={signedIn} />
      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        title={removing ? `Remove ${removing.user}?` : "Remove account?"}
        confirmLabel="Remove account"
        failed="Account not removed"
        body="LydiaBox deletes the stored sign-in of this account from the host. Bindings and routing groups that use only this subscription stop working until you sign in again."
        onConfirm={async () => {
          if (!removing) {
            return;
          }
          await models.removeAccount(removing.kind, removing.user);
          onChanged();
          toast.success(`${removing.user} removed`);
        }}
      />
    </Panel>
  );
}
