const code = String.raw`(?:\d{3}-\d{3}|\d{4,8})`;
const spanishLabel = String.raw`c[oó]digo\s+(?:de\s+(?:verificaci[oó]n|seguridad|confirmaci[oó]n|autenticaci[oó]n|acceso|un\s+solo\s+uso|WhatsApp|Telegram)|para\s+iniciar\s+sesi[oó]n)`;
const englishLabel = String.raw`(?:verification|security|confirmation|authentication|login|sign[- ]in|one[- ]time|WhatsApp|Telegram)\s+(?:code|passcode|pin)|one[- ]time\s+password`;
const label = `(?:${spanishLabel}|${englishLabel}|otp)`;
const codeAfterLabel = new RegExp(
  String.raw`\b${label}\b\s*(?:(?:is|es)\b\s*)?(?:[:=]\s*)?(${code})(?![\p{L}\p{N}]|[-./]\d|[ \t]+\d)`,
  "giu",
);
const codeBeforeLabel = new RegExp(
  String.raw`(?<![\p{L}\p{N}./+\-])(${code})\s+(?:is|es)\s+(?:(?:your|the|tu|su|el)\s+)?${label}\b`,
  "giu",
);

export function findVerificationCode(text: string): string | null {
  const candidates = new Set<string>();
  for (const pattern of [codeAfterLabel, codeBeforeLabel]) {
    for (const match of text.matchAll(pattern)) {
      candidates.add(match[1].replace("-", ""));
      // A message with two distinct candidates needs manual review.
      if (candidates.size > 1) return null;
    }
  }
  return candidates.values().next().value ?? null;
}
