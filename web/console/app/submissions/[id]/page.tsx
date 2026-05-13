'use client';

import { useEffect, useState } from 'react';

const BFF = process.env.BFF_URL ?? 'http://localhost:8090';

type Hints = {
  until_deadline_seconds: number;
  inside_cutoff_window: boolean;
  cutoff_seconds: number;
  allowed_actions: string[];
};

type View = {
  submission: any;
  transitions: any[];
  document?: any;
  version?: any;
  audit_events?: any[];
  ui: Hints;
};

export default function SubmissionPage({ params }: { params: { id: string } }) {
  const [view, setView] = useState<View | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [actor, setActor] = useState('operator@example.kz');

  const load = () => {
    fetch(`${BFF}/api/v1/submissions/${params.id}`)
      .then((r) => (r.ok ? r.json() : r.json().then((j) => Promise.reject(j))))
      .then(setView)
      .catch((e) => setErr(typeof e === 'string' ? e : JSON.stringify(e)));
  };

  useEffect(() => {
    load();
    const t = setInterval(load, 5000); // мягкий поллинг
    return () => clearInterval(t);
  }, [params.id]);

  const sendSignal = async (name: string, body: any) => {
    const res = await fetch(`${BFF}/api/v1/submissions/${params.id}/${name}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const j = await res.json().catch(() => ({}));
      alert(`Сигнал ${name} отклонён: ${j.error ?? res.status}`);
    } else {
      load();
    }
  };

  if (err) return <div className="card">Ошибка: {err}</div>;
  if (!view) return <div className="card">Загрузка…</div>;

  const s = view.submission;
  const h = view.ui;
  const inCutoff = h.inside_cutoff_window;

  return (
    <>
      <h1>Подача {s.id}</h1>
      <div className="card">
        <div className="row">
          <span className="badge">{s.state}</span>
          <span className="muted">
            {s.platform} • {s.tender_id} • {s.org_id}
          </span>
        </div>
        <p className="muted" style={{ marginTop: 8 }}>
          Дедлайн: <span className="kbd">{s.deadline_at}</span> (через {fmtSec(h.until_deadline_seconds)})
        </p>
        {inCutoff && (
          <p>
            <span className="badge warn">в окне отсечки T-{Math.round(h.cutoff_seconds / 60)} мин</span>{' '}
            автоматическая подача запрещена; нужно явное подтверждение.
          </p>
        )}
        {h.until_deadline_seconds <= 0 && (
          <p>
            <span className="badge bad">дедлайн истёк</span> подача невозможна.
          </p>
        )}
      </div>

      <div className="card">
        <h3>Действия</h3>
        <div className="row">
          <input
            value={actor}
            onChange={(e) => setActor(e.target.value)}
            style={{ maxWidth: 320 }}
            placeholder="actor email"
          />
        </div>
        <div className="row" style={{ marginTop: 12, flexWrap: 'wrap' }}>
          {h.allowed_actions.includes('review') && (
            <button onClick={() => sendSignal('review', { actor })}>Согласовать</button>
          )}
          {h.allowed_actions.includes('sign') && (
            <button
              onClick={() =>
                sendSignal('sign', {
                  actor,
                  esig_cert_cn: 'CN=Operator',
                  esig_cert_sha: 'devsha',
                })
              }
            >
              Подписать (ЭЦП)
            </button>
          )}
          {h.allowed_actions.includes('submit') && (
            <button
              className="primary"
              onClick={() => sendSignal('submit', { actor, idempotency_key: s.id + '-v1' })}
            >
              Подать
            </button>
          )}
          {h.allowed_actions.includes('submit_with_ack') && (
            <button
              className="primary"
              onClick={() => {
                if (!confirm('Вы внутри окна T-30 минут. Подтверждаете подачу?')) return;
                sendSignal('submit', {
                  actor,
                  idempotency_key: s.id + '-v1',
                  acknowledge_cutoff: true,
                });
              }}
            >
              Подать (acknowledge cutoff)
            </button>
          )}
          {h.allowed_actions.includes('cancel') && (
            <button
              onClick={() => {
                const reason = prompt('Причина отмены?') ?? '';
                sendSignal('cancel', { actor, reason });
              }}
            >
              Отменить
            </button>
          )}
          {h.allowed_actions.length === 0 && <span className="muted">Действий нет.</span>}
        </div>
      </div>

      {view.document && (
        <div className="card">
          <h3>Документ</h3>
          <p className="muted">
            {view.document.title} • {view.document.template_code} • статус{' '}
            <span className="badge">{view.document.status}</span>
          </p>
        </div>
      )}

      <div className="card">
        <h3>История переходов</h3>
        <table>
          <thead>
            <tr>
              <th>Когда</th>
              <th>Из</th>
              <th>В</th>
              <th>Кто</th>
              <th>Причина</th>
            </tr>
          </thead>
          <tbody>
            {(view.transitions ?? []).map((t, i) => (
              <tr key={i}>
                <td className="kbd">{t.occurred_at}</td>
                <td>{t.from_state}</td>
                <td>{t.to_state}</td>
                <td>{t.actor}</td>
                <td className="muted">{t.reason}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="card">
        <h3>Аудит-журнал (последние 20)</h3>
        <table>
          <thead>
            <tr>
              <th>Когда</th>
              <th>Действие</th>
              <th>Кто</th>
              <th>Trace</th>
            </tr>
          </thead>
          <tbody>
            {(view.audit_events ?? []).map((e, i) => (
              <tr key={i}>
                <td className="kbd">{e.occurred_at}</td>
                <td>{e.action}</td>
                <td>{e.actor_id}</td>
                <td className="kbd" style={{ maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {e.trace_id}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function fmtSec(sec: number): string {
  if (sec <= 0) return 'истёк';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h}ч ${m}м`;
  return `${m}м`;
}
