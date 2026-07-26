import http from "k6/http";
import { check } from "k6";

export const options = {
  scenarios: {
    ingestion: {
      executor: "constant-arrival-rate",
      rate: 100,
      timeUnit: "1s",
      duration: "30s",
      preAllocatedVUs: 20,
      maxVUs: 100,
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<250"],
  },
};

const endpointID = __ENV.ENDPOINT_ID;
const apiKey = __ENV.API_KEY;

export default function () {
  const key = `k6-${__VU}-${__ITER}`;
  const response = http.post(
    `${__ENV.BASE_URL || "http://localhost:8080"}/v1/events`,
    JSON.stringify({
      type: "load.test",
      payload: { vu: __VU, iteration: __ITER },
      endpoint_ids: [endpointID],
    }),
    {
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${apiKey}`,
        "Idempotency-Key": key,
      },
    },
  );
  check(response, { accepted: (r) => r.status === 202 });
}
