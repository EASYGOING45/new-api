// One instance per account. State is process-local; no cross-instance promises.
export function protectTransport(transport, {
  maxConcurrency = 2, failureThreshold = 2, cooldownMs = 30_000,
  firstEventMs = 60_000, totalMs = 90_000, now = Date.now,
} = {}) {
  let active = 0, failures = 0, until = 0, disabled = false, probe = false, epoch = 0;
  const snapshot = () => ({
    state: disabled ? 'disabled' : probe ? 'half_open' : until ? 'open' : 'closed',
    in_flight: active, consecutive_failures: failures,
    retry_after_s: Math.max(0, Math.ceil((until - now()) / 1000)),
    max_concurrency: maxConcurrency, first_event_timeout_ms: firstEventMs, total_timeout_ms: totalMs,
  });
  function unavailable(status, code) {
    return Response.json({ error: { type: 'upstream_error', code, message: code } },
      { status, headers: { 'retry-after': String(Math.max(1, snapshot().retry_after_s)) } });
  }
  return {
    snapshot,
    transport: {
      ...transport,
      async fetch(url, options = {}) {
        if (disabled) return unavailable(401, 'account_credential_invalid');
        if (probe || (until && now() < until)) return unavailable(503, 'account_cooling_down');
        if (active >= maxConcurrency) return unavailable(429, 'account_busy');
        const trial = until > 0;
        if (trial) probe = true;
        const enteredEpoch = epoch;
        active++;
        let finished = false, reader, firstTimer, totalTimer;
        const controller = new AbortController();
        const abortFromCaller = () => controller.abort(options.signal.reason);
        if (options.signal?.aborted) abortFromCaller();
        else options.signal?.addEventListener('abort', abortFromCaller, { once: true });
        const timeout = () => controller.abort(Object.assign(new Error('account_timeout'), { status: 504 }));
        firstTimer = setTimeout(timeout, firstEventMs);
        totalTimer = setTimeout(timeout, totalMs);
        function finish(status, retryAfter) {
          if (finished) return;
          finished = true;
          clearTimeout(firstTimer); clearTimeout(totalTimer);
          options.signal?.removeEventListener('abort', abortFromCaller);
          active--; if (trial) probe = false;
          if (status === 0) return; // Client abort and downstream cancellation are not account failures.
          if (status === 401 || status === 403) { disabled = true; epoch++; return; }
          if (status === 429 || status >= 500) {
            failures++;
            if (trial || status === 429 || failures >= failureThreshold) {
              let retryMs = Number(retryAfter) * 1000;
              if (!Number.isFinite(retryMs)) retryMs = Date.parse(retryAfter) - now();
              if (!Number.isFinite(retryMs) || retryMs < 0) retryMs = 0;
              until = Math.max(until, now() + Math.max(cooldownMs, retryMs));
              epoch++;
            }
          } else if (status >= 200 && status < 300 && enteredEpoch === epoch) {
            failures = 0; until = 0;
          } else if (trial && enteredEpoch === epoch) {
            // A request-specific 4xx proves the transport responds but does not heal the account.
            until = now() + cooldownMs;
          }
        }
        function bounded(promise) {
          const signal = controller.signal;
          if (signal.aborted) {
            void Promise.resolve(promise).catch(() => {});
            return Promise.reject(signal.reason);
          }
          return new Promise((resolve, reject) => {
            const abort = () => reject(signal.reason);
            signal.addEventListener('abort', abort, { once: true });
            Promise.resolve(promise).then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
          });
        }
        function failed(error) {
          finish(options.signal?.aborted ? 0 : (error?.status || 502));
          controller.abort();
          void reader?.cancel().catch(() => {});
          // Never expose raw transport exceptions (which may carry request credentials).
          return Object.assign(new Error('account_transport_failed'), { status: error?.status === 504 ? 504 : 502 });
        }
        try {
          if (controller.signal.aborted) throw controller.signal.reason;
          const response = await bounded(Promise.resolve(transport.fetch(url, { ...options, signal: controller.signal })).then(r => {
            if (controller.signal.aborted) { void r.body?.cancel().catch(() => {}); throw controller.signal.reason; }
            return r;
          }));
          if (!response.ok || !response.body) {
            finish(response.status, response.headers.get('retry-after'));
            return response;
          }
          reader = response.body.getReader();
          const isSSE = response.headers.get('content-type')?.includes('text/event-stream');
          let pending = '', done = false, explicitError = false;
          const decoder = new TextDecoder();
          function inspect(chunk) {
            if (!isSSE) return;
            pending += decoder.decode(chunk, { stream: true }).replace(/\r\n/g, '\n');
            if (pending.length > 1024 * 1024) throw new Error('account_frame_too_large');
            let end;
            while ((end = pending.indexOf('\n\n')) >= 0) {
              const frame = pending.slice(0, end); pending = pending.slice(end + 2);
              const data = frame.split('\n').filter(l => l.startsWith('data:')).map(l => l.slice(5).trimStart()).join('\n');
              if (!data) continue;
              clearTimeout(firstTimer);
              if (data === '[DONE]') done = true;
              else if (JSON.parse(data).error) explicitError = true;
            }
          }
          // Keep reading under the first-event deadline, including heartbeat-only streams.
          const body = new ReadableStream({
            async pull(out) {
              try {
                const chunk = await bounded(reader.read());
                if (chunk.done) {
                  if (isSSE && !done) throw new Error('account_incomplete_stream');
                  finish(explicitError ? 502 : 200); out.close(); return;
                }
                inspect(chunk.value);
                if (!isSSE) clearTimeout(firstTimer);
                out.enqueue(chunk.value);
                if (done) { finish(explicitError ? 502 : 200); out.close(); void reader.cancel().catch(() => {}); }
              } catch (error) { out.error(failed(error)); }
            },
            cancel() { finish(0); controller.abort(); return reader.cancel().catch(() => {}); },
          });
          return new Response(body, { status: response.status, headers: response.headers });
        } catch (error) { throw failed(error); }
      },
    },
  };
}
