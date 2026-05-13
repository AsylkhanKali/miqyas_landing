/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  env: {
    BFF_URL: process.env.BFF_URL ?? 'http://localhost:8090',
  },
};
export default nextConfig;
