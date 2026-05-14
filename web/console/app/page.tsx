'use client';

import { useEffect, useState } from 'react';

const BFF = process.env.BFF_URL ?? 'http://localhost:8090';

export default function Home() {
  const [subId, setSubId] = useState('');
  const [templates, setTemplates] = useState<any[]>([]);

  useEffect(() => {
    fetch(`${BFF}/api/v1/templates`)
      .then((r) => r.json())
      .then((j) => setTemplates(j.templates ?? []))
      .catch(() => {});
  }, []);

  return (
    <>
      <h1>Operator Console</h1>
      <p className="muted" style={{ maxWidth: 680, fontSize: 14, lineHeight: 1.6 }}>
        Внутренний инструмент для подготовки и подачи заявок. Подача не выполняется
        автоматически в окне 30&nbsp;минут до дедлайна — требуется явное подтверждение оператора.
      </p>

      <div className="card">
        <h3>Открыть подачу</h3>
        <div className="row">
          <input
            placeholder="UUID submission"
            value={subId}
            onChange={(e) => setSubId(e.target.value)}
            style={{ maxWidth: 400 }}
          />
          <a href={subId ? `/submissions/${subId}` : '#'}>
            <button className="primary" disabled={!subId}>
              Открыть
            </button>
          </a>
        </div>
        <p className="muted" style={{ marginTop: 8 }}>
          UUID берётся из ответа <span className="kbd">POST /api/v1/submissions</span>{' '}
          submission-сервиса.
        </p>
      </div>

      <div className="card">
        <h3>Шаблоны документов</h3>
        {templates.length === 0 && <p className="muted">Шаблоны не загружены.</p>}
        <table>
          <thead>
            <tr>
              <th>Код</th>
              <th>Название</th>
              <th>Правил</th>
            </tr>
          </thead>
          <tbody>
            {templates.map((t) => (
              <tr key={t.code}>
                <td className="kbd">{t.code}</td>
                <td>{t.name}</td>
                <td>{(t.rules ?? []).length}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
