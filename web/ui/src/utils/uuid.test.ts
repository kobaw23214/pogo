import { afterEach, describe, expect, it, vi } from "vitest";
import { randomUUID, type UUIDCryptoProvider } from "./uuid";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function fixedRandom(values: number[]): UUIDCryptoProvider {
  return {
    getRandomValues(bytes: Uint8Array) {
      expect(bytes).toBeInstanceOf(Uint8Array);
      expect(bytes.length).toBe(16);
      bytes.set(values);
      return bytes;
    },
  };
}

describe("secure UUID generation", () => {
  it("uses native randomUUID and preserves its required receiver", () => {
    const uuid = "00112233-4455-4677-8899-aabbccddeeff";
    const provider: UUIDCryptoProvider = {
      randomUUID: vi.fn(function (this: UUIDCryptoProvider) {
        expect(this).toBe(provider);
        return uuid;
      }),
      getRandomValues: vi.fn(() => { throw new Error("unexpected fallback"); }),
    };
    expect(randomUUID(provider)).toBe(uuid);
    expect(provider.randomUUID).toHaveBeenCalledOnce();
    expect(provider.getRandomValues).not.toHaveBeenCalled();
  });

  it("uses crypto.getRandomValues when HTTP exposes no native randomUUID", () => {
    const provider = fixedRandom(Array.from({ length: 16 }, (_, i) => i));
    vi.stubGlobal("crypto", provider);
    expect(randomUUID()).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
    expect(provider).not.toHaveProperty("randomUUID");
  });

  it.each([undefined, null, "not a function", false])("falls back when randomUUID is %s", native => {
    const provider = { ...fixedRandom(new Array(16).fill(0)), randomUUID: native };
    expect(randomUUID(provider)).toBe("00000000-0000-4000-8000-000000000000");
    expect(provider.randomUUID).toBe(native);
  });

  it("preserves fallback receiver and all random bits except the version and variant", () => {
    const provider: UUIDCryptoProvider = {
      getRandomValues: vi.fn(function (this: UUIDCryptoProvider, bytes: Uint8Array) {
        expect(this).toBe(provider);
        bytes.fill(0xff);
        return bytes;
      }),
    };
    const uuid = randomUUID(provider);
    expect(uuid).toBe("ffffffff-ffff-4fff-bfff-ffffffffffff");
    expect(uuid).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(provider.getRandomValues).toHaveBeenCalledOnce();
  });

  it("does not mutate the global crypto object or use weak randomness", () => {
    const provider = Object.freeze(fixedRandom(new Array(16).fill(0xab)));
    vi.stubGlobal("crypto", provider);
    const weakRandom = vi.spyOn(Math, "random").mockImplementation(() => { throw new Error("weak randomness"); });
    const time = vi.spyOn(Date, "now").mockImplementation(() => { throw new Error("timestamp entropy"); });
    expect(randomUUID()).toBe("abababab-abab-4bab-abab-abababababab");
    expect(globalThis.crypto).toBe(provider);
    expect(provider).not.toHaveProperty("randomUUID");
    expect(weakRandom).not.toHaveBeenCalled();
    expect(time).not.toHaveBeenCalled();
  });

  it.each([{}, { getRandomValues: null }, { getRandomValues: "not a function" }])("fails clearly when strong randomness is unavailable: %j", provider => {
    expect(() => randomUUID(provider)).toThrow("Secure UUID generation is unavailable");
  });

  it("fails clearly when the environment has no crypto object", () => {
    vi.stubGlobal("crypto", undefined);
    expect(() => randomUUID()).toThrow("Secure UUID generation is unavailable");
  });

  it("does not silently hide a failure from the native random generator", () => {
    const failure = new Error("native random generator unavailable");
    const provider = {
      randomUUID: vi.fn(() => { throw failure; }),
      getRandomValues: vi.fn(),
    };
    expect(() => randomUUID(provider)).toThrow(failure);
    expect(provider.getRandomValues).not.toHaveBeenCalled();
  });

  it("propagates failure from getRandomValues without creating an ID", () => {
    const failure = new Error("random generator unavailable");
    expect(() => randomUUID({ getRandomValues() { throw failure; } })).toThrow(failure);
  });
});
