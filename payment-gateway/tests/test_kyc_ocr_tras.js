#!/usr/bin/env node

/**
 * Real OCR smoke test for the back side of an RG/CNH document.
 *
 * Usage:
 *   node tests/test_kyc_ocr_tras.js
 *
 * Required:
 *   tests/tras.png
 *   CAPABILITY_OCR_URL in .env or environment
 *
 * Optional exact assertions:
 *   EXPECTED_FULL_NAME="Nome Completo"
 *   EXPECTED_CPF="12345678909"
 *   EXPECTED_BIRTH_DATE="1990-01-31"
 */

const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '..');
const imagePath = path.join(__dirname, 'tras.png');
let lastOcrText = '';
let lastFields = {};

function loadDotEnv(filePath) {
  if (!fs.existsSync(filePath)) return;
  const lines = fs.readFileSync(filePath, 'utf8').split(/\r?\n/);
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const idx = trimmed.indexOf('=');
    if (idx <= 0) continue;
    const key = trimmed.slice(0, idx).trim();
    let value = trimmed.slice(idx + 1).trim();
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    if (!process.env[key]) process.env[key] = value;
  }
}

function onlyDigits(value) {
  return String(value ?? '').replace(/\D/g, '');
}

function flattenObjects(value, output = []) {
  if (!value || typeof value !== 'object') return output;
  if (Array.isArray(value)) {
    for (const item of value) flattenObjects(item, output);
    return output;
  }
  output.push(value);
  for (const nested of Object.values(value)) flattenObjects(nested, output);
  return output;
}

function collectText(value, output = []) {
  if (!value || typeof value !== 'object') return output;
  if (Array.isArray(value)) {
    for (const item of value) collectText(item, output);
    return output;
  }
  for (const [key, nested] of Object.entries(value)) {
    if (/parsedtext|text|raw_text|ocr_text/i.test(key) && typeof nested === 'string') {
      output.push(nested);
    }
    if (nested && typeof nested === 'object') collectText(nested, output);
  }
  return output;
}

function pick(records, keys) {
  for (const record of records) {
    for (const key of keys) {
      const value = record[key];
      if (value != null && String(value).trim()) return String(value).trim();
    }
  }
  return '';
}

function extractFields(response) {
  const records = flattenObjects(response);
  let fullName = pick(records, [
    'full_name',
    'fullName',
    'name',
    'nome',
    'nome_completo',
    'holder_name',
    'holderName',
  ]);
  let cpf = onlyDigits(pick(records, [
    'cpf',
    'document_number',
    'documentNumber',
    'tax_id',
    'taxId',
    'numero_cpf',
    'cpf_number',
  ])).slice(0, 11);
  let birthDate = pick(records, [
    'birth_date',
    'birthDate',
    'date_of_birth',
    'dateOfBirth',
    'data_nascimento',
    'nascimento',
  ]);
  const ocrText = collectText(response).join('\n');
  lastOcrText = ocrText;
  if (!cpf) {
    const cpfLabelMatch = ocrText.match(/CPF\s*[\r\n\s:.-]*([0-9.\s/-]{11,18})/i);
    const match = cpfLabelMatch?.[1] || ocrText.match(/\b\d{3}[.\s/-]?\d{3}[.\s/-]?\d{3}[.\s/-]?\d{2}\b/)?.[0];
    cpf = onlyDigits(match || '').slice(0, 11);
  }
  if (!birthDate) {
    const datePattern = /\b(?:0?[1-9]|[12]\d|3[01])[\/\-.](?:0?[1-9]|1[0-2])[\/\-.](?:19|20)?\d{2}\b/;
    const lines = ocrText
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean);
    const naturalidadeIndex = lines.findIndex((line) => /NATURALIDADE/i.test(line));
    if (naturalidadeIndex >= 0) {
      birthDate = lines.slice(naturalidadeIndex + 1, naturalidadeIndex + 5).map((line) => line.match(datePattern)?.[0]).find(Boolean) || '';
    }
    if (!birthDate) {
      const match = ocrText.match(datePattern);
      birthDate = match?.[0] || '';
    }
  }
  if (!fullName) {
    const lines = ocrText
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean);
    const nomeIndex = lines.findIndex((line) => /\bNOME\b|NOME\s+CIVIL|NOME\s+COMPLETO/i.test(line));
    if (nomeIndex >= 0) {
      fullName = lines.slice(nomeIndex + 1).find((line) => /^[A-ZÀ-Ú][A-ZÀ-Ú\s]{5,}$/.test(line) && !/\bCPF\b|\bRG\b|\bDATA\b|\bNASC/i.test(line)) || '';
    }
    if (!fullName) {
      fullName = lines.find((line) => /^[A-ZÀ-Ú][A-ZÀ-Ú\s]{8,}$/.test(line) && !/\bREP[ÚU]BLICA\b|\bBRASIL\b|\bCPF\b|\bIDENTIDADE\b|\bREGISTRO\b/i.test(line)) || '';
    }
  }
  lastFields = { full_name: fullName, cpf, birth_date: birthDate };
  return { full_name: fullName, cpf, birth_date: birthDate, raw_text: ocrText };
}

function assertField(name, actual, expected) {
  if (!actual) throw new Error(`OCR nao retornou ${name}`);
  if (expected && String(actual).toLowerCase() !== String(expected).toLowerCase()) {
    throw new Error(`${name} diferente. esperado=${expected} recebido=${actual}`);
  }
}

async function main() {
  loadDotEnv(path.join(root, '.env'));

  const endpoint = process.env.CAPABILITY_OCR_URL;
  if (!endpoint) {
    throw new Error('CAPABILITY_OCR_URL nao configurado no .env');
  }
  if (!fs.existsSync(imagePath)) {
    throw new Error(`Imagem nao encontrada: ${imagePath}`);
  }

  const fileBase64 = fs.readFileSync(imagePath).toString('base64');
  const form = new URLSearchParams();
  form.set('apikey', process.env.CAPABILITY_OCR_API_KEY || '');
  form.set('base64image', `data:image/png;base64,${fileBase64}`);
  form.set('language', 'por');
  form.set('isOverlayRequired', 'false');
  form.set('detectOrientation', 'true');
  form.set('scale', 'true');
  form.set('OCREngine', '2');

  const started = Date.now();
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: form.toString(),
  });
  const rawText = await response.text();
  let parsed;
  try {
    parsed = rawText ? JSON.parse(rawText) : {};
  } catch {
    throw new Error(`OCR retornou resposta nao JSON: ${rawText.slice(0, 300)}`);
  }
  if (!response.ok) {
    throw new Error(`OCR HTTP ${response.status}: ${rawText.slice(0, 500)}`);
  }

  const fields = extractFields(parsed);
  assertField('full_name', fields.full_name, process.env.EXPECTED_FULL_NAME);
  assertField('cpf', fields.cpf, onlyDigits(process.env.EXPECTED_CPF || ''));
  assertField('birth_date', fields.birth_date, process.env.EXPECTED_BIRTH_DATE);

  const { raw_text, ...publicFields } = fields;
  console.log(JSON.stringify({
    ok: true,
    image: path.relative(root, imagePath),
    latency_ms: Date.now() - started,
    fields: publicFields,
  }, null, 2));
}

main().catch((error) => {
  const debug = process.env.OCR_DEBUG === '1';
  console.error(JSON.stringify({
    ok: false,
    error: error.message,
    image: path.relative(root, imagePath),
    hint: debug ? 'Rode sem OCR_DEBUG depois de ajustar o parser.' : 'Use OCR_DEBUG=1 para ver o texto OCR bruto.',
    fields: lastFields,
    raw_text: debug ? lastOcrText.slice(0, 3000) : undefined,
  }, null, 2));
  process.exit(1);
});
