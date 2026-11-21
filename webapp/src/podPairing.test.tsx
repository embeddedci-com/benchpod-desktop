// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { PodPairing } from "./PodPairing";

beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as never;
  window.matchMedia ??= ((q: string) => ({
    matches: false,
    media: q,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent: () => false,
  })) as never;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const json = (data: unknown) =>
  new Response(JSON.stringify(data), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });

describe("PodPairing", () => {
  it("pairs over USB, then over the cloud pod the app knows", async () => {
    const sent: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string, init?: RequestInit) => {
        if (path === "/api/pod/pairing")
          return json({
            pod_name: "benchpod-30a9f1",
            usb: "usb:benchpod-30a9f1",
            cloud: [
              {
                name: "benchpod-30a9f1",
                online: true,
                connection: "embeddedci:benchpod-30a9f1",
              },
            ],
            cloud_pick: "embeddedci:benchpod-30a9f1",
            offline: false,
          });
        sent.push(JSON.parse(String(init?.body)));
        return json({ ok: true, text: "Paired", pod: { connected: true } });
      }),
    );
    const onPaired = vi.fn();
    render(
      <MantineProvider>
        <PodPairing onPaired={onPaired} />
      </MantineProvider>,
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Pair this computer" }),
    );
    await waitFor(() => expect(onPaired).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Paired and connected.")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Pair through embeddedci.com" }),
    );
    await waitFor(() => expect(onPaired).toHaveBeenCalledTimes(2));
    expect(sent).toEqual([
      { connection: "usb:benchpod-30a9f1" },
      { connection: "embeddedci:benchpod-30a9f1" },
    ]);
  });

  it("on the LAN, pairs over the app's own connection and shows the CLI's message", async () => {
    const sent: unknown[] = [];
    const msg =
      "This computer is not paired with the pod at 192.168.1.164:8080, and the pod is not plugged into this computer over USB.";
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string, init?: RequestInit) => {
        if (path === "/api/pod/pairing")
          return json({ usb: "192.168.1.164", cloud: [], offline: false });
        sent.push(JSON.parse(String(init?.body)));
        return json({ ok: false, text: msg, pod: { connected: false } });
      }),
    );
    render(
      <MantineProvider>
        <PodPairing />
      </MantineProvider>,
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Pair this computer" }),
    );
    expect(await screen.findByText(msg)).toBeTruthy();
    expect(sent).toEqual([{ connection: "192.168.1.164" }]);
  });

  it("without a cloud way, says to switch LAN pairing off", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ usb: "usb", cloud: [], offline: false })),
    );
    render(
      <MantineProvider>
        <PodPairing />
      </MantineProvider>,
    );
    expect(await screen.findByText(/Switch LAN pairing off/)).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Pair through embeddedci.com" }),
    ).toBeNull();
  });
});
