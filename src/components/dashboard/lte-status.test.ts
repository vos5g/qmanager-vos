import { describe, expect, test } from "bun:test";
import { nrOnCell } from "./lte-status";

const na = { upper_layer_ind: false, restrict_dcnr: false, endc_enabled: true, serving: "LTE" };

describe("nrOnCell", () => {
  test("explains why 5G is or isn't available", () => {
    expect(nrOnCell(na, "LTE")).toBe("not_offered");
    expect(nrOnCell({ ...na, upper_layer_ind: true }, "LTE")).toBe("offered");
    expect(nrOnCell({ ...na, upper_layer_ind: true, restrict_dcnr: true }, "LTE")).toBe("blocked");
    expect(nrOnCell({ ...na, endc_enabled: false }, "LTE")).toBe("off_on_device");
    expect(nrOnCell(na, "5G-NSA")).toBe("in_use");
  });
});
