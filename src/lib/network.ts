export function networkAddressCount(cidr: string): bigint {
  const [address, bits] = cidr.split("/");
  return BigInt(1) << BigInt((address.includes(":") ? 128 : 32) - Number(bits));
}
