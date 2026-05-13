import './globals.css';
import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'Procurement Operator Console',
  description: 'Internal console for procurement operations',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ru">
      <body>
        <header className="topbar">
          <strong>Operator Console</strong>
          <nav>
            <a href="/">Главная</a>
            <a href="/templates">Шаблоны</a>
          </nav>
        </header>
        <main className="container">{children}</main>
      </body>
    </html>
  );
}
