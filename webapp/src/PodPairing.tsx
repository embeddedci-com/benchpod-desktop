import { useEffect, useState } from "react";
import { HelpTip } from "@bpui";
import {
  Alert,
  Button,
  Group,
  Loader,
  Select,
  Stack,
  Text,
} from "@mantine/core";
import { api, type AppState, type PairingOptions } from "./api";

/**
 * This computer is not paired with the pod (firmware 4.0.1+ over Ethernet or Wi-Fi, BP0081): pair
 * it over the pod's USB-C cable, or through embeddedci.com when signed in, then connect again.
 */
export function PodPairing({
  onPaired,
}: {
  onPaired?: (pod: AppState["pod"]) => void;
}) {
  const [opts, setOpts] = useState<PairingOptions | null>(null);
  const [cloudConn, setCloudConn] = useState<string | null>(null);
  const [busy, setBusy] = useState<"" | "usb" | "cloud">("");
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(
    null,
  );

  useEffect(() => {
    let live = true;
    api
      .podPairing()
      .then((o) => {
        if (!live) return;
        setOpts(o);
        setCloudConn(
          o.cloud_pick ?? (o.cloud.length === 1 ? o.cloud[0].connection : null),
        );
      })
      .catch(() => live && setOpts({ usb: "usb", cloud: [], offline: false }));
    return () => {
      live = false;
    };
  }, []);

  const pair = async (via: "usb" | "cloud", connection: string) => {
    setBusy(via);
    setResult(null);
    try {
      const r = await api.pairPod(connection);
      const stillLocked = r.ok && r.pod.compat === "not_paired";
      const text = stillLocked
        ? "Paired, but the pod still refuses this computer. Check you paired the same pod."
        : r.ok && !r.pod.connected
          ? r.pod.error || r.text
          : r.text;
      setResult({ ok: r.ok && r.pod.connected, text });
      if (r.ok) onPaired?.(r.pod);
    } catch (e) {
      setResult({ ok: false, text: (e as Error).message });
    } finally {
      setBusy("");
    }
  };

  return (
    <Stack gap="xs">
      <Alert
        color="orange"
        variant="light"
        title="This computer is not paired with the pod"
      >
        <Text size="sm">
          Pair it once to use the pod over the network.{" "}
          <HelpTip
            label="LAN pairing"
            docs="/docs/benchpod-security#lan-pairing"
          >
            Over Ethernet and Wi-Fi the pod takes commands only from paired
            computers. Others see its status only. USB and embeddedci.com need
            no pairing.
          </HelpTip>
        </Text>
      </Alert>
      {!opts ? (
        <Group gap="xs">
          <Loader size="xs" />
        </Group>
      ) : (
        <>
          <Group gap="xs">
            <Button
              onClick={() => void pair("usb", opts.usb)}
              loading={busy === "usb"}
              disabled={busy !== ""}
            >
              Pair this computer
            </Button>
            <Text size="xs" c="dimmed">
              Plug in the pod&apos;s USB-C cable first{" "}
              <HelpTip
                label="pair over USB-C"
                docs="/docs/benchpod-security#lan-pairing"
              >
                Use the USB-C IN port on the back of the pod, once. The app
                pairs{" "}
                {opts.pod_name ||
                  (opts.usb === "usb" || opts.usb.startsWith("usb:")
                    ? "the pod on USB"
                    : "the pod it is connected to")}
                , then connects again over the network. You can unplug the cable
                afterwards.
              </HelpTip>
            </Text>
          </Group>
          {opts.cloud.length > 0 ? (
            <Group gap="xs" align="flex-end">
              {opts.cloud.length > 1 || !cloudConn ? (
                <Select
                  size="xs"
                  placeholder="Pick the pod"
                  data={opts.cloud.map((d) => ({
                    value: d.connection,
                    label: d.online ? d.name : `${d.name} (offline)`,
                  }))}
                  value={cloudConn}
                  onChange={setCloudConn}
                  w={220}
                />
              ) : null}
              <Button
                variant="light"
                onClick={() => cloudConn && void pair("cloud", cloudConn)}
                loading={busy === "cloud"}
                disabled={busy !== "" || !cloudConn}
              >
                Pair through embeddedci.com
              </Button>
              <HelpTip
                label="pair through embeddedci.com"
                docs="/docs/benchpod-security#lan-pairing"
              >
                No cable needed: embeddedci.com sends this computer&apos;s key
                to the pod. The pod must be online, and you must be an admin of
                its organization.
              </HelpTip>
            </Group>
          ) : (
            <Text size="xs" c="dimmed">
              No cable at hand? Switch LAN pairing off in the pod&apos;s
              Settings on embeddedci.com.{" "}
              <HelpTip
                label="LAN pairing off"
                docs="/docs/benchpod-security#lan-pairing"
              >
                Sign in to embeddedci.com in the app to pair a registered pod
                from there. With LAN pairing off, any computer on the network
                may use the pod, as before firmware 4.0.1.
              </HelpTip>
            </Text>
          )}
        </>
      )}
      {result ? (
        <Alert color={result.ok ? "green" : "red"} variant="light">
          <Text size="sm" style={{ whiteSpace: "pre-wrap" }}>
            {result.ok
              ? "Paired and connected."
              : result.text || "Pairing failed."}
          </Text>
        </Alert>
      ) : null}
    </Stack>
  );
}
