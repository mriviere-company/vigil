// Burst load test: hammers the `dev-free` key (2 req/s, burst 5 —
// gateway/internal/ratelimit) with concurrent VUs well past that rate for
// 30s, and asserts the gateway's rate-limit behavior is correct under
// contention: 429s appear with a usable Retry-After header and the
// normalized error envelope (docs/api.md), permitted requests still succeed,
// and nothing 5xxs.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_KEY = 'dev-free'; // free tier: 2 req/s, burst 5 — docs/api.md

const rateLimited = new Counter('rate_limited_total');
const permitted = new Counter('permitted_total');
const serverErrors = new Counter('server_error_total');
const unexpectedStatus = new Counter('unexpected_status_total');

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  scenarios: {
    burst: {
      // 10 VUs looping with no think time against a 2 req/s + burst-5 key is
      // an order of magnitude past capacity — exactly the "well past 2 req/s"
      // the brief calls for.
      executor: 'constant-vus',
      vus: 10,
      duration: '30s',
    },
  },
  thresholds: {
    // At least N 429s observed...
    rate_limited_total: ['count>=20'],
    // ...and zero 5xx, ever.
    server_error_total: ['count==0'],
    unexpected_status_total: ['count==0'],
    // Gate on the check assertions themselves (429s carry Retry-After and
    // the normalized envelope, 200s carry a verdict body). Without this, a
    // regression in any of those would only lower the printed "checks
    // passed" percentage — the three counter thresholds above are keyed on
    // status codes alone and independent of check outcomes, so the run
    // would still exit 0.
    checks: ['rate==1'],
  },
};

export default function () {
  const res = http.post(
    `${BASE_URL}/v1/moderate/text`,
    JSON.stringify({ content: 'Burst load test payload, please classify this text.' }),
    { headers: { 'Content-Type': 'application/json', 'X-API-Key': API_KEY } }
  );

  if (res.status === 429) {
    rateLimited.add(1);
    check(res, {
      '429 has Retry-After header': (r) => {
        const v = r.headers['Retry-After'];
        return v !== undefined && Number(v) >= 1;
      },
      '429 has normalized error envelope': (r) => {
        try {
          const body = JSON.parse(r.body);
          return (
            body.error !== undefined &&
            typeof body.error.code === 'string' &&
            (body.error.code === 'rate_limited' || body.error.code === 'quota_exceeded') &&
            typeof body.error.request_id === 'string'
          );
        } catch (e) {
          return false;
        }
      },
    });
  } else if (res.status === 200) {
    permitted.add(1);
    check(res, {
      '200 has a verdict body': (r) => {
        try {
          const body = JSON.parse(r.body);
          return typeof body.verdict === 'string';
        } catch (e) {
          return false;
        }
      },
    });
  } else if (res.status >= 500) {
    serverErrors.add(1);
  } else {
    unexpectedStatus.add(1);
  }
}

export function handleSummary(data) {
  const text = renderSummary(data);
  return {
    'loadtest/results/burst-summary.json': JSON.stringify(data, null, 2),
    'loadtest/results/burst-summary.txt': text,
    stdout: text,
  };
}

function renderSummary(data) {
  const m = data.metrics;
  const count = (name) => (m[name] ? m[name].values.count : 0);
  const lines = [];
  lines.push('=== vigil k6 burst summary ===');
  lines.push(`http_reqs: ${m.http_reqs ? m.http_reqs.values.count : 'n/a'}`);
  lines.push(`permitted (200): ${count('permitted_total')}`);
  lines.push(`rate_limited (429): ${count('rate_limited_total')}`);
  lines.push(`server_errors (5xx): ${count('server_error_total')}`);
  lines.push(`unexpected_status: ${count('unexpected_status_total')}`);
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
