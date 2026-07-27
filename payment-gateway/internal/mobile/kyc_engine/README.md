# ChainFX KYC Engine

`internal/mobile/kyc_engine` concentra a camada interna de análise KYC e biometria facial do mobile.

Ela não guarda imagem como biometria primária. O objetivo do pacote é gerar um `face embedding`, criptografar esse vetor e permitir comparação recorrente em operações sensíveis.

## Fluxo

```text
Mobile
  -> captura frente do documento
  -> captura verso do documento
  -> captura video facial guiado
  -> upload autenticado para backend/Cloudinary
  -> POST /api/mobile/kyc/submit

Backend
  -> cria kyc_request
  -> publica kyc.submitted
  -> KYCWorker chama kyc_engine.Analyze
  -> salva kyc_analysis_results
  -> se approved, salva user_face_biometrics.face_embedding_encrypted
```

O video facial guiado substitui a selfie separada no fluxo oficial. A instrução esperada no app é:

```text
Olhe para a camera.
Vire levemente para esquerda.
Vire para direita.
Pisque.
Volte ao centro.
```

## O Que a Engine Entrega

- `Decision`: `approved`, `manual_review` ou `rejected`.
- `Score`: score final de 0 a 100.
- `DocumentScore`: qualidade/consistência do documento.
- `FaceMatchScore`: comparação documento versus rosto capturado.
- `LivenessScore`: qualidade de prova de vida por video.
- `ReplayRiskScore`: risco de foto de tela, replay ou captura fraudulenta.
- `DuplicateScore`: risco de mesmo rosto em outra conta.
- `RiskScore`: risco contextual por IP, device fingerprint e metadados.
- `LatencyMS`: latência da análise.
- `EmbeddingHash`: HMAC do embedding para comparação sem expor o vetor.
- `Embedding`: vetor facial em memória, nunca serializado no JSON.

## Persistência

Tabelas relacionadas:

```text
kyc_requests
kyc_analysis_results
user_face_biometrics
kyc_risk_events
```

Campos sensíveis:

```text
user_face_biometrics.face_embedding_encrypted
user_face_biometrics.embedding_hash
```

`face_embedding_encrypted` é criptografado com AES-GCM usando:

```text
FACE_BIOMETRY_SECRET
LGPD_SECRET
WEBHOOK_SECRET
MOBILE_JWT_SECRET
```

Use `FACE_BIOMETRY_SECRET` em produção. Os fallbacks existem para desenvolvimento e compatibilidade operacional.

## Endpoints Relacionados

```text
POST /api/mobile/uploads/kyc
POST /api/mobile/kyc/submit
GET  /api/mobile/kyc/engine/status
GET  /api/mobile/kyc/engine/metrics
POST /api/mobile/biometry/verify
```

`/api/mobile/biometry/verify` recebe uma nova selfie ou video facial e compara contra o embedding salvo. Ele retorna similaridade, liveness, replay risk, decisão e latência.

## Antifraude

A base atual contempla:

- flags de documento incompleto;
- ausência de video facial;
- marcadores de replay/screen capture no input;
- device fingerprint ausente;
- duplicidade por `embedding_hash`;
- registro de eventos em `kyc_risk_events`;
- métricas de latência via `/api/mobile/kyc/engine/metrics`.

## Limite Atual

A implementação atual é uma base determinística e plugável. Ela é útil para:

- contrato backend/mobile;
- persistência segura;
- criptografia;
- auditoria;
- testes antibug;
- métricas de latência;
- integração com worker assíncrono.

Ela ainda não é um detector biométrico regulatório completo. Para produção bancária real, plugar providers/modelos em etapas:

1. OCR real de documento.
2. Detecção de rosto no documento.
3. Extração real de frames do video.
4. Face embedding real.
5. Liveness real por movimento/piscada/pose.
6. Classificador antifraude para replay, deepfake e tela.

## Provider Real de Produção

Quando `KYC_ENGINE_PROVIDER_URL` está definido, `NewFromEnv` usa um provider HTTP externo em vez do modo determinístico.

Variáveis:

```text
KYC_ENGINE_PROVIDER_URL=https://kyc-provider.internal
KYC_ENGINE_PROVIDER_API_KEY=...
FACE_BIOMETRY_SECRET=...
```

`KYC_ENGINE_PROVIDER_URL` aceita URL base ou URL completa com `/analyze`.

Contrato enviado ao provider:

```json
{
  "RequestID": "uuid",
  "UserID": "uuid",
  "Level": 1,
  "DocumentURL": "https://...",
  "DocumentBackURL": "https://...",
  "SelfieURL": "https://...",
  "FacialVideoURL": "https://...",
  "DeviceFingerprint": "...",
  "IPAddress": "...",
  "UserAgent": "..."
}
```

Contrato esperado de resposta:

```json
{
  "provider": "chainfx_local_ai",
  "model_version": "chainfx-local-ai-service-v1",
  "decision": "approved",
  "score": 94,
  "document_score": 96,
  "face_match_score": 92,
  "liveness_score": 91,
  "replay_risk_score": 4,
  "duplicate_score": 100,
  "risk_score": 8,
  "latency_ms": 1300,
  "embedding": [0.12, -0.44],
  "embedding_hash": "optional",
  "flags": [],
  "details": {}
}
```

`embedding` precisa ser retornado pelo provider para o backend salvar `face_embedding_encrypted`. Se vier ausente, a decisão `approved` é rebaixada para `manual_review`.

## Provider Local Self-Hosted

Existe uma implementação de referência sem AWS/GCP/vendor KYC em uma pasta isolada na raiz:

```text
chainfx-kyc-provider/
```

Ela é o contrato do nosso provider local. Em produção, conectar modelos próprios:

- `FACE_EMBEDDING_ONNX`: modelo local para embedding facial.
- `LIVENESS_ONNX`: modelo local para prova de vida/replay/deepfake.
- OCR local ou `OCR_PROVIDER_URL`: leitura estruturada do documento.
- Detector local de face no documento e no video.

Instalação local:

```bash
cd chainfx-kyc-provider
pip install -r requirements.txt
set KYC_PROVIDER_API_KEY=local-secret
set FACE_EMBEDDING_ONNX=C:\models\face_embedding.onnx
set LIVENESS_ONNX=C:\models\liveness.onnx
python main.py
```

Backend:

```bash
set KYC_ENGINE_PROVIDER_URL=http://127.0.0.1:9097/analyze
set KYC_ENGINE_PROVIDER_API_KEY=local-secret
set FACE_BIOMETRY_SECRET=<secret-forte>
```

Sem modelos reais configurados, o provider retorna `manual_review` e flag `local_models_not_configured`. Isso evita aprovar usuário fingindo biometria bancária.

Estrutura do serviço:

```text
chainfx-kyc-provider/main.py                     entrypoint
chainfx-kyc-provider/src/kyc_local_ai/app.py     HTTP Flask: /health e /analyze
chainfx-kyc-provider/src/kyc_local_ai/config.py  env vars e thresholds
chainfx-kyc-provider/src/kyc_local_ai/media.py   download temporário das mídias Cloudinary
chainfx-kyc-provider/src/kyc_local_ai/quality.py qualidade de documento/imagem
chainfx-kyc-provider/src/kyc_local_ai/ocr.py     hook de OCR local
chainfx-kyc-provider/src/kyc_local_ai/liveness.py análise de vídeo, movimento e replay
chainfx-kyc-provider/src/kyc_local_ai/face.py    face embedding e comparação facial
chainfx-kyc-provider/src/kyc_local_ai/pipeline.py score final e decisão
```

Tecnicamente, a biometria via vídeo funciona assim:

1. O app grava um vídeo guiado com rosto central, virada para esquerda, virada para direita, piscada e retorno ao centro.
2. O provider baixa o vídeo pelo URL assinado/privado entregue pelo backend.
3. `liveness.py` extrai frames, mede movimento entre frames e, em produção, roda `LIVENESS_ONNX` para detectar replay, tela, deepfake, ausência de piscada ou pose inválida.
4. `face.py` escolhe frames bons, detecta/corta rosto e roda `FACE_EMBEDDING_ONNX` para gerar o vetor facial.
5. O rosto do documento também é detectado e transformado em embedding.
6. A comparação é feita por similaridade entre embeddings.
7. O provider retorna score, embedding do vídeo, liveness, face match, flags e latência.
8. O backend criptografa o embedding e salva em `user_face_biometrics`.
9. Verificações futuras usam `/api/mobile/biometry/verify`, gerando novo embedding e comparando contra o embedding criptografado salvo.

Decisão de produção:

```text
approved       somente com modelos reais disponíveis, score alto e sem flags críticas
manual_review  quando há dúvida, modelos ausentes, baixa qualidade ou inconsistência moderada
rejected       quando score é baixo, liveness falha ou face match é muito ruim
```

## Teste de Eficiência

Script:

```powershell
.\scripts\kyc_engine_efficiency.ps1 -BaseUrl http://localhost:8080 -Token $env:MOBILE_ACCESS_TOKEN -Runs 20
```

Ele consulta `/api/mobile/kyc/engine/metrics`, mede latência HTTP e salva um JSON local com:

- média HTTP;
- máximo HTTP;
- média da engine;
- p95 da engine;
- máximo da engine;
- amostras por execução.

Teste direto do provider cloud:

```powershell
.\scripts\kyc_cloud_probe.ps1 -BaseUrl https://chainfx-biometry-engine-production.up.railway.app -ApiKey $env:KYC_ENGINE_PROVIDER_API_KEY
```

Esse probe valida `/health`, chama `/analyze`, mede latência HTTP e verifica se o provider retorna `embedding` e `embedding_hash`.

## Testes

```bash
go test ./internal/mobile/kyc_engine
go test ./internal/mobile ./internal/workers
```

Os testes garantem:

- embedding determinístico;
- score dentro de 0..100;
- latência não negativa;
- criptografia/descriptografia do embedding;
- similaridade perfeita após decrypt do mesmo vetor.

## Segurança e LGPD

- Não logar embedding, segredo, vídeo ou URL privada.
- Não versionar `.env`.
- Rotacionar secrets expostos.
- Manter consentimento explícito para biometria.
- Permitir exclusão/anonymização da conta.
- Restringir acesso administrativo às evidências KYC.
- Tratar `reviewer_notes` e `details` como dados sensíveis.
