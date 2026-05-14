import http from 'k6/http';
import { check } from 'k6';

export let options = {
  stages: [
    { duration: '10s', target: 100 },
    { duration: '30s', target: 100 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<200'],
    http_req_failed: ['rate<0.01'],
  },
};

// Each VU maintains its own counter to ensure unique dedup keys.
// 100 VUs * ~30 iterations = 3000 unique incidents possible.
let counters = {};

export default function () {
  let vu = __VU;
  if (counters[vu] === undefined) counters[vu] = 0;
  let id = ++counters[vu];

  let payload = JSON.stringify({
    rule:     `rule_${vu}_${id}`,
    service:  `svc_${vu}_${id}`,
    metric:   `m_${vu}_${id}`,
    value:    0.85 + Math.random() * 0.1,
    severity: 'critical',
    message:  'Load test',
    timestamp: Date.now() / 1000,
  });

  let res = http.post('http://localhost:8082/api/v1/notifications',
    payload, { headers: { 'Content-Type': 'application/json' } });
  check(res, { 'created': (r) => r.status === 201 || r.status === 202 });
}