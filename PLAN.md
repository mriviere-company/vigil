# vigil — API de modération de contenu pour PME

> Plan d'exécution destiné à Claude Code. Suivre les phases dans l'ordre.
> Chaque phase se termine par des critères d'acceptation vérifiables avant de passer à la suivante.

---

## 1. Contexte et objectif

**Problème métier** : les PME avec du contenu généré par les utilisateurs (avis clients, marketplaces, forums, uploads d'images) ont une obligation de modération mais n'ont ni le budget d'une équipe dédiée, ni les compétences pour intégrer du ML. Les solutions existantes sont chères ou complexes à intégrer.

**Produit** : une API REST où un client envoie un texte ou une image et reçoit un verdict en < 200ms (p95) : `approved` / `flagged` / `rejected`, avec score de confiance et catégories (toxicité, spam, contenu explicite).

**Objectif portfolio** : démontrer des compétences AI Infrastructure Engineering — serving ML en production, résilience, observabilité, IaC, maîtrise des coûts. Le ML n'est pas le sujet ; le serving l'est.

**Stack imposée** : Go (gateway), Python (inférence), Docker, Terraform, AWS (ECS Fargate).

---

## 2. Architecture cible

```
                        ┌─────────────────────────────────────────┐
                        │              AWS (VPC)                   │
  Client PME            │                                          │
  (clé API) ──HTTPS──▶  ALB ──▶ Gateway Go (Fargate, subnets privés)
                        │           │ rate limit / auth / circuit breaker
                        │           ├──▶ Inference-Text Python (Fargate)
                        │           └──▶ Inference-Image Python (Fargate)
                        │                                          │
                        │  ECR · CloudWatch Logs · Prometheus /metrics
                        └─────────────────────────────────────────┘
```

**Services** :

| Service | Langage | Rôle |
|---|---|---|
| `gateway` | Go 1.22+ | Auth par clé API, rate limiting, routing, circuit breaker, backpressure, métriques, logs structurés |
| `inference-text` | Python 3.12 / FastAPI | Classification toxicité/spam (distilbert-type via ONNX Runtime) |
| `inference-image` | Python 3.12 / FastAPI | Détection NSFW (petit modèle ONNX) |

**Flux nominal** : `POST /v1/moderate/text` → gateway (auth → rate limit → circuit breaker) → service d'inférence → verdict agrégé → réponse JSON.

**Mode dégradé** : si un service d'inférence est down (circuit ouvert), la gateway répond `202 { "status": "pending", "retry_after": 30 }` au lieu de bloquer le client.

---

## 3. Contrat d'API (à figer en Phase 0)

### Endpoints publics (gateway)

```
POST /v1/moderate/text
  Headers: X-API-Key: <key>
  Body: { "content": "string", "language": "fr|en" (optionnel) }
  200: {
    "id": "uuid",
    "verdict": "approved|flagged|rejected",
    "score": 0.97,
    "categories": { "toxicity": 0.02, "spam": 0.01 },
    "latency_ms": 43
  }
  202: { "status": "pending", "retry_after": 30 }   # mode dégradé
  401 / 429 / 400 avec corps d'erreur normalisé

POST /v1/moderate/image
  Body: { "image_base64": "..." } ou multipart
  Réponse: même schéma, categories = { "nsfw": ..., "violence": ... }

GET /v1/health        # santé gateway + état des circuits
GET /metrics          # Prometheus (non exposé publiquement via ALB)
```

### Endpoints internes (services Python)

```
POST /predict   { "content" | "image_base64" } → { "scores": {...}, "model_version": "..." }
GET  /health    → { "status": "ok", "model_loaded": true }
```

**Règle de verdict (gateway, configurable)** : `rejected` si un score > 0.85, `flagged` si > 0.5, sinon `approved`.

**Tiers de rate limiting (multi-tenancy)** :

| Tier | Limite | Burst |
|---|---|---|
| free | 1 000 req/mois, 2 req/s | 5 |
| pro | 100 000 req/mois, 20 req/s | 50 |

Clés API stockées en config/env pour ce projet (pas de DB — assumer et documenter ce choix : hors scope, remplaçable par DynamoDB).

Limitation assumée : sans DB, les quotas *mensuels* ne sont que des compteurs en mémoire (remis à zéro au redémarrage) — seul le débit (req/s + burst) est réellement garanti. À documenter dans « Limitations & next steps ».

---

## 4. Structure du repo

```
vigil-api/
├── README.md                  # Problem → Architecture → Metrics → Costs
├── docker-compose.yml         # stack locale complète
├── Makefile                   # cibles: build, test, lint, up, down, load-test
├── gateway/
│   ├── cmd/gateway/main.go
│   ├── internal/
│   │   ├── auth/              # validation clé API + tiers
│   │   ├── ratelimit/         # token bucket par clé
│   │   ├── circuit/           # circuit breaker
│   │   ├── proxy/             # clients HTTP vers inference (timeouts, retries)
│   │   ├── verdict/           # agrégation scores → verdict
│   │   ├── middleware/        # logging, metrics, recovery, request-id
│   │   └── server/            # routing, graceful shutdown
│   ├── Dockerfile             # multi-stage → distroless
│   └── go.mod
├── inference/
│   ├── common/                # code partagé (schemas Pydantic, logging)
│   ├── text/
│   │   ├── app/main.py
│   │   ├── app/model.py       # chargement ONNX, prédiction
│   │   ├── tests/
│   │   └── Dockerfile         # multi-stage, non-root
│   └── image/                 # même structure
├── infra/
│   ├── modules/
│   │   ├── network/           # VPC, subnets pub/priv, NAT, SG
│   │   ├── ecs-service/       # module générique réutilisé 3x
│   │   ├── alb/
│   │   └── ecr/
│   ├── environments/
│   │   └── dev/               # main.tf, backend.tf (S3 + use_lockfile), variables
│   └── README.md              # comment déployer / détruire
├── loadtest/                  # scripts k6
└── .github/workflows/
    ├── ci.yml                 # lint + tests + build + scan
    └── deploy.yml             # (optionnel) push ECR + terraform apply manuel
```

---

## 5. Phases d'exécution

> Chaque phase indique le modèle Claude et l'effort de raisonnement recommandés (règle CLAUDE.md « Model/effort routing ») : exécuter la phase sur ce tier, escalader d'un cran (effort d'abord, puis modèle) seulement si la phase résiste.

### Phase 0 — Fondations (courte)

> **Modèle** : Haiku 4.5 · effort **low** — scaffolding mécanique, le contrat d'API est déjà entièrement spécifié en §3.

- [ ] Init repo, structure des dossiers, Makefile squelette
- [ ] Figer le contrat d'API ci-dessus dans `docs/api.md` (source de vérité)
- [ ] `docker-compose.yml` vide-mais-valide, `.editorconfig`, `.gitignore`

**Acceptation** : `make help` fonctionne, contrat d'API commité.

### Phase 1 — Service d'inférence texte (Python)

> **Modèle** : Sonnet 5 · effort **medium** — service FastAPI/ONNX standard, bien spécifié.

- [ ] FastAPI + Pydantic v2, endpoints `/predict` et `/health`
- [ ] Modèle : distilbert toxicité exporté/récupéré en ONNX, inférence via `onnxruntime` (CPU)
- [ ] Pattern : le modèle derrière une interface (`ModelBackend` protocol) → un `StubModel` déterministe pour les tests, le vrai modèle en prod. Injection au démarrage via variable d'env
- [ ] Chargement du modèle au startup (lifespan), `/health` reflète `model_loaded`
- [ ] Logging structuré JSON (stdlib logging + formatter), request-id propagé depuis header
- [ ] Tests pytest : fixtures avec StubModel, tests du contrat de réponse, test de validation d'entrée (contenu vide, trop long), test /health
- [ ] Dockerfile multi-stage (builder → slim), utilisateur non-root, HEALTHCHECK

**Acceptation** : `pytest` vert, conteneur < 800 Mo, `curl /predict` répond en local via compose.

### Phase 2 — Service d'inférence image (Python)

> **Modèle** : Sonnet 5 · effort **low** — clonage d'une structure existante + factorisation dans `common/`.

- [ ] Cloner la structure de `text/`, factoriser dans `inference/common/`
- [ ] Petit modèle NSFW ONNX, validation d'entrée (taille max, formats acceptés)
- [ ] Mêmes exigences de tests et Dockerfile

**Acceptation** : idem Phase 1. Refactoring commun fait (pas de copier-coller entre text et image).

### Phase 3 — Gateway Go (cœur du projet)

> **Modèle** : Opus 4.8 · effort **high** — concurrence Go (circuit breaker maison, backpressure, tests -race), le cœur du projet. Escalader vers Fable 5 uniquement si un bug de concurrence résiste.

Ordre de construction interne :

1. **Serveur HTTP de base** : `net/http` + `chi` (ou stdlib mux 1.22), graceful shutdown (context + signal), middleware request-id, recovery, logging `slog` JSON
2. **Auth** : middleware clé API → résolution du tier, clés en config YAML/env
3. **Rate limiting** : token bucket par clé API (`golang.org/x/time/rate` ou implémentation maison documentée), réponse 429 avec `Retry-After`
4. **Proxy vers inférence** : client HTTP avec timeouts explicites (connect/total), retries avec backoff+jitter sur erreurs 5xx/réseau (max 2), propagation request-id
5. **Circuit breaker** : par service aval (text, image). États closed/open/half-open, seuils configurables. Implémentation maison (c'est un point d'entretien — la coder soi-même avec tests vaut mieux qu'importer sony/gobreaker, mais documenter l'alternative)
6. **Verdict** : agrégation scores → verdict selon seuils configurables
7. **Backpressure** : semaphore (channel buffered) limitant les requêtes in-flight vers chaque service aval ; au-delà → 503 ou 202 pending selon config
8. **Métriques Prometheus** : histogrammes latence par endpoint (p50/p95/p99 via buckets), compteurs requêtes/verdicts/429/circuits ouverts, gauge in-flight

**Tests Go (exigence forte)** :
- Table-driven tests pour verdict, ratelimit, auth
- Circuit breaker : tests des transitions d'état avec horloge injectée (interface `Clock`)
- `httptest` : serveur d'inférence mocké → tests end-to-end de la gateway (nominal, 429, circuit ouvert → 202, timeout aval)
- Race detector activé (`go test -race ./...`)
- Cible : coverage > 80% sur `internal/`

**Dockerfile** : multi-stage → `gcr.io/distroless/static`, binaire statique < 20 Mo (mesuré 2026-07-12 : binaire 10,6 Mo ; image décompressée 21,6 Mo car la base distroless pèse ~10 Mo décompressée — gate CI ajusté à 25 Mo en conséquence).

**Acceptation** : `make test` vert avec -race, compose complet fonctionnel (gateway + 2 services), scénario manuel : tuer inference-text → la gateway répond 202 puis récupère (half-open → closed).

### Phase 4 — Stack locale et load test

> **Modèle** : Sonnet 5 · effort **medium** — configuration compose/Prometheus/Grafana et scripts k6.

- [ ] `docker-compose.yml` final : 3 services + Prometheus + Grafana (provisionné avec un dashboard JSON commité)
- [ ] Script k6 : montée en charge, vérification p95 < 200ms en local, scénario burst (dépassement rate limit)
- [ ] `make load-test` documenté

**Acceptation** : dashboard Grafana montre les métriques pendant le load test ; captures d'écran pour le README.

### Phase 5 — Infra Terraform

> **Modèle** : Sonnet 5 · effort **medium** — Terraform standard, mais coûts réels : relecture humaine du plan Terraform avant tout `apply`.

- [ ] Backend remote : S3 + verrouillage natif `use_lockfile` (initialement S3+DynamoDB ; migré le 2026-07-11, `dynamodb_table` déprécié par Terraform 1.15 — bucket créé via un mini-script bootstrap documenté)
- [ ] Module `network` : VPC, 2 AZ, subnets publics (ALB) / privés (Fargate), NAT gateway *unique* (choix de coût documenté vs 1 par AZ)
- [ ] Module `ecs-service` générique (image, cpu/mem, port, env, health check, autoscaling target CPU) → instancié 3 fois
- [ ] Module `alb` : HTTPS optionnel (ACM si domaine dispo, sinon HTTP documenté comme limitation), target group → gateway uniquement ; `/metrics` bloqué au niveau ALB
- [ ] ECR + lifecycle policy (garder 5 images)
- [ ] CloudWatch : log groups avec rétention 7 jours, alarme p95 > 200ms (SNS optionnel)
- [ ] Autoscaling Fargate : min 1 / max 3 par service, target CPU 70%
- [ ] `terraform fmt` / `validate` / `tflint` propres ; variables typées et documentées ; outputs utiles (URL ALB)
- [ ] `infra/README.md` : coût mensuel estimé, procédure apply/destroy, rappel que `terraform destroy` doit être exécuté après chaque session

**Acceptation** : `terraform apply` depuis zéro → API fonctionnelle via l'URL ALB → `terraform destroy` propre. Chronométrer et noter dans le README (« infra complète en X minutes »).

### Phase 6 — CI/CD

> **Modèle** : Sonnet 5 · effort **low** — YAML CI standard.

- [ ] `ci.yml` : jobs parallèles — (a) Go : vet, staticcheck ou golangci-lint, test -race + gate de coverage > 80 % sur `internal/` ; (b) Python : ruff, mypy, pytest ; (c) Terraform : fmt -check, validate, tflint + scan sécurité IaC (`trivy config` ou tfsec) ; (d) build des 3 images + scan Trivy (fail sur CRITICAL) ; (e) gitleaks sur tout l'historique (`fetch-depth: 0`)
- [ ] Hygiène workflows : `permissions:` minimales sur le GITHUB_TOKEN, actions tierces pinnées par SHA
- [ ] Dependabot : `gomod`, `pip`, `docker`, `github-actions` (updates hebdomadaires)
- [ ] Réglages GitHub (manuels, gratuits en repo public) : secret scanning + push protection activés, branch protection sur `main` exigeant la CI verte
- [ ] Badge CI dans le README
- [ ] `deploy.yml` **requis** (c'est le mécanisme de démo à la demande : apply avant un entretien, destroy après) : workflow_dispatch manuel → build/push ECR avec tag SHA → `terraform apply` (documenter OIDC GitHub→AWS plutôt que clés statiques)

**Acceptation** : CI verte sur main, PR de test montrant un échec de lint bloqué, push de test avec un faux secret bloqué (gitleaks / push protection).

### Phase 7 — README et finition portfolio

> **Modèle** : Opus 4.8 · effort **medium** — rédaction portfolio : la qualité du texte est le livrable, c'est ce que le recruteur lit en premier.

Structure imposée du README racine :

1. **Problem** (3 phrases, le besoin PME)
2. **Solution** (le produit en 2 phrases + exemple curl avec réponse réelle)
3. **Architecture** (diagramme — Mermaid ou image — + justification d'un paragraphe par choix : pourquoi Go, pourquoi circuit breaker, pourquoi Fargate)
4. **Performance** (captures Grafana, résultats k6, p95 mesuré)
5. **Cost analysis** (coût AWS mensuel détaillé, coût par 1 000 inférences, comparaison avec le prix qu'on facturerait — l'économie unitaire, + tableau des scénarios d'optimisation : Fargate Spot, subnets publics sans NAT, scale-to-zero nocturne)
6. **Failure modes** (tableau : panne → comportement observé → mécanisme)
7. **Run it** (compose local en 2 commandes, déploiement AWS en 3)
8. **Limitations & next steps** (honnête : clés API en config, pas de DB, HTTP si pas de domaine, modèles non fine-tunés)

**Acceptation** : un dev inconnu peut lancer le projet en local en < 5 minutes en suivant le README.

---

## 6. Standards transverses (à respecter dans toutes les phases)

- **Commits** : conventionnels (`feat:`, `fix:`, `test:`, `infra:`), petits, message expliquant le *pourquoi*
- **Go** : pas de package `utils`, erreurs wrappées (`fmt.Errorf("...: %w", err)`), contextes propagés partout, pas de variable globale mutable
- **Python** : type hints partout, `ruff` en config stricte, Pydantic pour toute frontière I/O
- **Config** : 12-factor, tout par variables d'environnement, valeurs par défaut saines pour le local
- **Sécurité** : conteneurs non-root, pas de secret commité, `.env.example` fourni, scan Trivy en CI, gitleaks en CI + push protection GitHub
- **Chaque design pattern implémenté** (circuit breaker, token bucket, backpressure, graceful shutdown) doit avoir un court commentaire de tête expliquant le choix — c'est du matériel d'entretien

## 7. Garde-fous coûts AWS

- Fargate min capacity 1, tailles minimales (0.25 vCPU / 512 Mo gateway ; 0.5 vCPU / 1 Go inférence)
- 1 seul NAT gateway (~32 $/mois si laissé tourner — d'où le destroy systématique)
- Budget AWS avec alerte à 20 $/mois provisionné via Terraform dans `infra/environments/guardrails/` (racine persistante, jamais détruite — voir `infra/README.md`, « Cost guardrails »)
- Règle : `terraform destroy` en fin de chaque session de travail (sauf `infra/environments/guardrails/`) ; le README doit prouver que la reconstruction est triviale

## 8. Définition de « terminé »

- [ ] CI verte, coverage Go > 80% internal/, tests Python verts
- [ ] Démo locale complète via compose en 2 commandes
- [ ] Apply → test API réelle sur AWS → destroy, sans intervention manuelle
- [ ] README complet avec métriques réelles et analyse de coûts
- [ ] Scénario de panne démontré et documenté (kill inference → 202 → récupération)
