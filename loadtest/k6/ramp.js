// Ramp load test: verifies the gateway holds p95 < 200ms under a sustained,
// realistic arrival rate (PLAN.md Phase 4 acceptance).
//
// Design choice — constant-arrival-rate, not raw VU looping: a `ramping-vus`
// scenario has each VU fire the next request as soon as the previous one
// returns, so latency and offered load are coupled (a slowdown throttles the
// arrival rate itself, hiding the true saturation point). `ramping-arrival-
// rate` decouples them: k6 targets a fixed requests/second schedule and
// allocates whatever VUs it needs (up to maxVUs) to sustain it, so latency
// measurements reflect the server under a known, independent offered load.
//
// The target rate is capped at 15 req/s — comfortably under the `dev-pro`
// key's 20 req/s / burst 50 tier limit (gateway/internal/ratelimit) — so the
// rate limiter's own 429s don't pollute the latency numbers this test exists
// to produce.
import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_KEY = 'dev-pro'; // pro tier: docs/api.md

// Non-2xx/202 responses count against this rate; 202 (circuit-open degraded
// mode) is expected-degraded behavior, not a failure, so it is excluded.
const errorRate = new Rate('errors');

// Mixed benign/toxic/spam-like payloads so verdicts vary (approved/flagged/
// rejected) rather than exercising a single code path.
const payloads = [
  'This product exceeded my expectations, fast shipping and great quality.',
  'I really appreciate the customer support team, they resolved my issue in minutes.',
  'Thanks for the quick delivery, the packaging was neat, secure, and on time.',
  'The weather today is quite pleasant for a long walk in the park with the kids.',
  'Our team shipped the quarterly report a day ahead of schedule this time.',
  'You are a disgusting idiot and I hate everyone who ever made this garbage app.',
  'I cannot believe how rude the staff was, absolutely awful and hateful service.',
  'Everyone who works here should be ashamed, I hate this company and its people.',
  'Buy now! Limited time offer, click this link to claim your free prize immediately!!!',
  'This is spam, buy now, click here, act now before it is too late, buy now!',
];

export const options = {
  // Ensures p(99) is present in the JSON/text summary alongside the default
  // avg/min/med/max/p(90)/p(95).
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  scenarios: {
    ramp: {
      executor: 'ramping-arrival-rate',
      startRate: 0,
      timeUnit: '1s',
      preAllocatedVUs: 20,
      maxVUs: 50,
      stages: [
        { target: 15, duration: '1m' }, // ramp 0 -> 15 req/s
        { target: 15, duration: '2m' }, // hold
        { target: 0, duration: '30s' }, // ramp down
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<200'],
    errors: ['rate<0.01'],
  },
};

export default function () {
  const content = payloads[Math.floor(Math.random() * payloads.length)];
  const res = http.post(
    `${BASE_URL}/v1/moderate/text`,
    JSON.stringify({ content }),
    { headers: { 'Content-Type': 'application/json', 'X-API-Key': API_KEY } }
  );

  // 202 = degraded mode (downstream circuit open) — an expected outcome
  // under load, not an error; tagged/checked accordingly rather than folded
  // into http_req_failed.
  const ok = check(res, {
    'status is 200 (verdict) or 202 (degraded)': (r) => r.status === 200 || r.status === 202,
  });
  errorRate.add(!ok);
}

export function handleSummary(data) {
  const text = renderSummary(data);
  return {
    'loadtest/results/ramp-summary.json': JSON.stringify(data, null, 2),
    'loadtest/results/ramp-summary.txt': text,
    stdout: text,
  };
}

// renderSummary builds a small human-readable report without depending on an
// external jslib import (kept self-contained so the script has no network
// dependency beyond the target under test).
function renderSummary(data) {
  const m = data.metrics;
  const fmtMs = (v) => (v === undefined || v === null ? 'n/a' : v.toFixed(2) + 'ms');
  const lines = [];
  lines.push('=== vigil k6 ramp summary ===');
  lines.push(`http_reqs: ${m.http_reqs ? m.http_reqs.values.count : 'n/a'}`);
  if (m.http_req_duration) {
    const d = m.http_req_duration.values;
    lines.push(
      `http_req_duration: avg=${fmtMs(d.avg)} p50=${fmtMs(d.med)} p90=${fmtMs(d['p(90)'])} ` +
        `p95=${fmtMs(d['p(95)'])} p99=${fmtMs(d['p(99)'])} max=${fmtMs(d.max)}`
    );
  }
  if (m.errors) {
    lines.push(`error rate (excl. 202): ${(m.errors.values.rate * 100).toFixed(2)}%`);
  }
  if (m.checks) {
    lines.push(`checks passed: ${(m.checks.values.rate * 100).toFixed(2)}%`);
  }
  lines.push('');
  lines.push('thresholds:');
  for (const [name, metric] of Object.entries(m)) {
    if (metric.thresholds) {
      for (const [expr, result] of Object.entries(metric.thresholds)) {
        lines.push(`  ${name} ${expr}: ${result.ok ? 'PASS' : 'FAIL'}`);
      }
    }
  }
  return lines.join('\n') + '\n';
}
