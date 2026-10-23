# podwatch

Agente em Go que procura Pods com problema, pede uma causa provável ao Grok e publica a hipótese num canal do Slack.

A varredura fala só com a API do Kubernetes. A API do modelo entra apenas quando existe um Pod novo para alertar.

## Como funciona

1. A cada `POLL_INTERVAL` (padrão 1 minuto) o processo lista os Pods. Fora do cluster ele usa o kubeconfig padrão (`KUBECONFIG` ou `~/.kube/config`). Dentro do cluster usa a ServiceAccount.
2. A classificação é local. Pod saudável, Pod `Succeeded` e Pod em exclusão não seguem adiante.
3. Para cada problema que ainda não foi alertado, o agente lê os eventos desse Pod e um trecho de log dos containers afetados.
4. Esse relato vai numa única chamada a `POST /v1/responses` do modelo (`XAI_MODEL`, padrão `grok-4.7`). Não há ferramentas nem continuação da conversa. O pedido manda `store: false`.
5. A resposta vira uma mensagem no Slack: sinal, causa provável, indícios e o que checar. O texto deixa claro que é uma hipótese.

Se o modelo falha, o Slack ainda recebe o sinal do Pod, com a nota de que a análise automática não veio. Se o Slack falha, o Pod não entra no silêncio e a próxima varredura tenta de novo.

O silêncio fica na memória do processo. Duas réplicas mandariam a mesma mensagem. O Deployment em `deploy/podwatch.yaml` usa uma réplica e estratégia `Recreate`.

### O que conta como problema

Alerta na hora:

- espera com `CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, `CreateContainerConfigError`, `CreateContainerError`, `InvalidImageName`, `RunContainerError` ou `DeadlineExceeded`
- container terminado com `OOMKilled`, `Error`, `ContainerCannotRun`, `StartError` ou outro exit diferente de zero
- fase `Failed` ou `Unknown`

`ErrImagePull` e `ImagePullBackOff` dividem a mesma chave de silêncio.

Depois de `STARTUP_GRACE` (padrão 2 minutos):

- `ContainerCreating`, `PodInitializing` ou `CreateContainer`
- `Pending` e `Unschedulable`
- `Running` que continua `NotReady`

Um init container que ainda está rodando não gera `Pending`, para uma migration longa não disparar alerta. Um container `Ready`, mesmo com um OOM antigo no estado anterior, também não alerta. Se ele está em `CrashLoop` e o estado anterior foi `OOMKilled`, os dois entram no relato.

### O que vai para o modelo

Status do Pod, dono, labels `app` e `app.kubernetes.io/*`, condições, até 12 eventos e logs dos containers com problema (no máximo dois). Cada leitura pede `LOG_TAIL_LINES` linhas e no máximo `MAX_LOG_BYTES` bytes. O relato inteiro é cortado em 24 mil caracteres.

Antes do envio, o texto passa por um mascaramento de senha, Bearer, JWT, chave `AKIA` e token `xoxb-` / `xoxp-`. O nome de um Secret ausente, como `secret "db" not found`, permanece.

A chamada de modelo só acontece se o motivo ainda está fora de `ALERT_COOLDOWN` e cabe em `MAX_ALERTS_PER_SCAN`. Os mais graves vão primeiro. Com o cluster quieto, o gasto de token é zero.

## Variáveis de ambiente

Durações usam o formato do Go: `30s`, `1m`, `2h`. Inteiros precisam ser positivos. Booleanos aceitam `1`, `true`, `yes`, `y`, `sim` e `0`, `false`, `no`, `n`, `nao`, `não`.

| Variável | Obrigatória | Padrão | Função |
| --- | --- | --- | --- |
| `XAI_API_KEY` | sim | | Chave da API. Também é exigida em `--dry-run`, porque o modelo continua sendo chamado. Crie em [console.x.ai](https://console.x.ai/team/default/api-keys). |
| `XAI_MODEL` | não | `grok-4.7` | Modelo da chamada. |
| `XAI_BASE_URL` | não | `https://api.x.ai/v1` | URL base. Precisa ser `https`. |
| `XAI_TIMEOUT` | não | `180s` | Tempo máximo de uma chamada ao modelo. |
| `SLACK_WEBHOOK_URL` | um dos modos | | Incoming Webhook. Precisa ser `https`. |
| `SLACK_BOT_TOKEN` | um dos modos | | Token `xoxb-…` para `chat.postMessage`. O bot precisa estar no canal. |
| `SLACK_CHANNEL` | junto com o token | | ID do canal, por exemplo `C0123456789`. |
| `NAMESPACES` | não | todos | Lista separada por vírgula. Vazio lista o cluster inteiro, menos os excluídos. |
| `EXCLUDE_NAMESPACES` | não | `kube-system,kube-public,kube-node-lease` | Namespaces ignorados. Se a variável existir e estiver vazia, nenhum namespace é excluído. |
| `POLL_INTERVAL` | não | `1m` | Intervalo entre varreduras. |
| `ALERT_COOLDOWN` | não | `30m` | Silêncio do mesmo Pod com o mesmo motivo. |
| `STARTUP_GRACE` | não | `2m` | Espera antes de alertar Pending, criação lenta, NotReady e Unschedulable. |
| `LOG_TAIL_LINES` | não | `80` | Linhas de log pedidas por container. |
| `MAX_LOG_BYTES` | não | `12000` | Bytes máximos de cada leitura de log. |
| `MAX_ALERTS_PER_SCAN` | não | `8` | Alertas novos por varredura. |
| `DRY_RUN` | não | `false` | Imprime a mensagem no stdout e não chama o Slack. Nesse modo as variáveis do Slack podem ficar vazias. |
| `ONCE` | não | `false` | Faz uma varredura e encerra. |

Defina só um destino de Slack: o webhook, ou o par token e canal. Os dois juntos fazem o processo recusar a configuração. Em `DRY_RUN` ou `--dry-run` o destino pode faltar.

As flags `--once` e `--dry-run` ligam o mesmo comportamento que `ONCE` e `DRY_RUN`. Elas não desligam o valor que veio do ambiente.

O arquivo [`.env.example`](.env.example) lista as mesmas chaves.

## Rodar local

```bash
cp .env.example .env
```

Preencha `XAI_API_KEY` e `SLACK_WEBHOOK_URL`. Para o primeiro teste, limite o escopo com `NAMESPACES`.

```bash
set -a && source .env && set +a
go run ./cmd/podwatch --once --dry-run
```

Sem `--dry-run`, a mensagem vai para o Slack. Sem `--once`, o processo fica no loop até `SIGINT` ou `SIGTERM`.

```bash
go test ./...
```

## Rodar no cluster

O manifesto cria o namespace `podwatch`, a ServiceAccount, a permissão de `get/list/watch` em pods e events, `get` em `pods/log`, e o Deployment.

```bash
docker build -t podwatch:dev .
kubectl -n podwatch create secret generic podwatch \
  --from-literal=XAI_API_KEY='...' \
  --from-literal=SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...'
kubectl apply -f deploy/podwatch.yaml
```

A imagem do manifesto é `podwatch:dev` com `imagePullPolicy: IfNotPresent`. Num kind ou minikube, carregue essa imagem no nó antes do apply. O Secret entra por `envFrom`. Intervalo, graça, cooldown, namespaces excluídos e o teto de alertas estão no Deployment e podem ser alterados lá.
