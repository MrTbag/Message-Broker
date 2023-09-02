import grpc from 'k6/net/grpc';
import { check, sleep } from 'k6';
import encoding from 'k6/encoding';

const client = new grpc.Client();
client.load(['/my-app/api/proto'], 'broker.proto');

export const options = {
  discardResponseBodies: true,
  scenarios: {
    contacts: {
      executor: 'constant-vus',
      vus: 3000,
      duration: '10s',
    },
  },
};

export default () => {
  client.connect('localhost:5052', {
    plaintext: true,
  });

  
  
  const data = { subject: 'my-subject', body: encoding.b64encode('Hello, gRPC!'), expirationSeconds: 60};
  const response = client.invoke('broker.Broker/Publish', data);

  check(response, {
    'status is OK': (r) => r && r.status === grpc.StatusOK,
  });

  console.log(JSON.stringify(response.message));

  client.close();
  sleep(1);
};
