import { buildApp } from './app';
import { config } from './config';

const app = buildApp({ sharedSecret: config.sharedSecret });

app
  .listen({ port: config.port, host: '0.0.0.0' })
  .then(() => {
    app.log.info(`olt-cli-gateway listening on :${config.port}`);
  })
  .catch((err) => {
    app.log.error(err);
    process.exit(1);
  });

process.on('SIGTERM', async () => {
  await app.close();
  process.exit(0);
});
