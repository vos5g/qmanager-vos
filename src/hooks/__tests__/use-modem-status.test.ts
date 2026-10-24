import { describe, expect, test } from "bun:test";
import { statusAgeSeconds } from "../use-modem-status";

describe("statusAgeSeconds", () => {
  test("ignores the stick's clock and counts from when the timestamp changes", () => {
    const t0 = Date.UTC(2026, 9, 24, 17, 0, 0);
    // A stick still at 1970: its timestamp is ~11000, decades behind the browser.
    expect(statusAgeSeconds(11000, t0)).toBe(0);
    expect(statusAgeSeconds(11000, t0 + 4000)).toBe(4);
    expect(statusAgeSeconds(11005, t0 + 5000)).toBe(0); // poller wrote again
    expect(statusAgeSeconds(11005, t0 + 16000)).toBe(11); // poller stopped
  });
});
