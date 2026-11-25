import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useDecoMutations } from "../api/hooks";
import type { DecoStatus } from "../api/types";
import { DecoForm } from "../components/DecoForm";
import { Button, Confirm, useToast } from "../components/ui";

/** Shown instead of the app while no Deco is connected: add one, or fix
 * the stored one when Atlantis could not start it. */
export function Connect({ deco }: { deco: DecoStatus }) {
  const m = useDecoMutations();
  const toast = useToast();
  const navigate = useNavigate();
  const [confirmRemove, setConfirmRemove] = useState(false);
  const fixing = deco.configured;
  const mut = fixing ? m.change : m.connect;
  return (
    <main className="grid min-h-dvh place-items-center px-4 py-8">
      <div className="w-full max-w-md space-y-4 rounded-lg border border-line bg-shoal p-6">
        <div>
          <h1 className="text-xl font-semibold">{fixing ? "Reconnect your Deco" : "Connect your Deco"}</h1>
          <p className="mt-1 text-sm text-drift">
            {fixing
              ? "Atlantis could not connect to the saved Deco. Check its address and password."
              : "Atlantis watches and manages your mesh through the main Deco, over your home network."}
          </p>
        </div>
        <DecoForm
          current={fixing ? deco : undefined}
          busy={mut.isPending}
          error={mut.error}
          submitLabel={fixing ? "Reconnect" : "Connect"}
          onSubmit={(c) =>
            mut.mutate(c, {
              onSuccess: (d) => {
                toast(d.discovery?.model ? `Connected to Deco ${d.discovery.model}` : "Connected to the Deco");
                navigate("/", { replace: true });
              },
            })
          }
        />
        {fixing && (
          <div className="border-t border-line pt-4">
            <Button tone="quiet" onClick={() => setConfirmRemove(true)}>
              Remove this Deco instead
            </Button>
          </div>
        )}
      </div>
      <Confirm
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title="Remove the Deco?"
        body={<p>Atlantis forgets this Deco and deletes its devices, history and events. Notification channels stay.</p>}
        action="Remove"
        typed="REMOVE"
        busy={m.remove.isPending}
        onConfirm={() => m.remove.mutate(undefined, { onSuccess: () => (setConfirmRemove(false), toast("Deco removed")) })}
      />
    </main>
  );
}
