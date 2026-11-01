export interface UUIDCryptoProvider {
  randomUUID?: unknown;
  getRandomValues?: unknown;
}

/** Generate an RFC 4122 v4 UUID in both secure and ordinary HTTP contexts. */
export function randomUUID(provider: UUIDCryptoProvider | undefined = globalThis.crypto): string {
  if (typeof provider?.randomUUID === "function") {
    return provider.randomUUID.call(provider) as string;
  }
  if (typeof provider?.getRandomValues !== "function") {
    throw new Error("Secure UUID generation is unavailable: this browser does not provide crypto.getRandomValues().");
  }

  const bytes = new Uint8Array(16);
  provider.getRandomValues.call(provider, bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
