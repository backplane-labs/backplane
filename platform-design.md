# backplane — platform design

Этот документ сохраняет исходный подробный дизайн и историческую разбивку по
этапам. Актуальное руководство на английском, границы реализации, примеры и
эксплуатация опубликованы в [документации](https://gopherex.github.io/backplane/)
(исходники: [website/docs](website/docs)). Описания этапов ниже не являются
текущим статусом релиза. Контракты API и пакетов сверяются с кодом в
[API reference](docs/api-reference.md) и [web/README.md](web/README.md).

## Границы Go SDK и подключение драйверов

`pkg/backplane`, `config`, `deps`, `hook`, `activity`, `event`, `route`,
`wsproto` и `backplanetest` не импортируют Temporal SDK, Nexus, NATS,
Consul, PostgreSQL или Valkey. CI проверяет эту границу через `go list -deps`
в `TestSDKDependencyBoundary`. Общими остаются protobuf/gRPC, отражение
схем, конфигурация, lifecycle и OpenTelemetry.

Драйвер выбирают явно в точке сборки приложения:

```go
import "github.com/gopherex/backplane/pkg/backplane/drivers/standard"

svc, err := backplane.Open(ctx, NewState, standard.Drivers())
```

`standard.Drivers()` включает адаптеры `drivers/consul`, `drivers/nats`
и `drivers/temporal`; каждый подключается только при заданной конфигурации.
Можно передать только нужные `consul.Driver()`, `nats.Driver()` или
`temporal.Driver()`. Без опций базовый SDK работает с локальной конфигурацией,
HTTP/gRPC, lifecycle и телеметрией. Настроенный NATS/Temporal/Consul без
установленного драйвера — ошибка конфигурации, а не молчаливое отключение.
Для standalone `config.Open` Consul подключается опцией `consul.Config()`.
Фабрики создают отдельный транспорт для каждого сервиса; глобального
реестра и регистрации через `init` нет. Конфигурационный источник и
discovery Consul используют отдельные HTTP-клиенты с одинаковыми настройками.

`workflows` — явное расширение для нативного Temporal: `Declare`, `Register`,
`Client`, `Activity` и `CallHook(ctx, ref, input, opts...)`. Обычные
`hook.Ref.Call` и `activity.Handle` используют нейтральные контракты.
`drivers/nats.JetStream(scope)` даёт нативный JetStream; общий `event`
оставляет публикацию, реакторы и redrive. Для тестов нативных workflow
используется `workflows/workflowtest.New(t, harness)`, для обычных
компонентов достаточно `backplanetest`.

Это разделение импортов и компиляции внутри существующего Go-модуля.
`go.mod` пока общий с сервером и нативными адаптерами; отдельные модули,
теги и независимый выпуск не вводятся этим изменением. Сервер запускает подсистемы по конфигурации: PostgreSQL, Consul и Valkey обязательны;
Temporal, NATS, observability и gateway включаются отдельно. Отключённые
подсистемы не запускают фоновые процессы и не входят в readiness.

NATS URL имеет тип `config.Secret`, поскольку может содержать логин,
пароль или токен. Для программного присваивания нужен `config.Secret(url)`;
транспорт получает значение через `Reveal`, effective config и логирование
маскируют его целиком. Это относится и к `config.NATS`, и к `infra/nats.Config`.

## 0. Что такое backplane

Стек без backplane полноценен: Consul, NATS, Temporal, PostgreSQL.
backplane ничего из этого не заменяет, не оборачивает и не деплоит.

backplane — место, где **независимые сервисы соединяются в установку**.
Сервис объявляет, что ему *нужно*, что он *умеет*, что *отправляет* и как
*настраивается*; кто с кем соединён и с какими значениями — решает
установка в консоли, а не код. Сервисы не импортируют друг друга:
хуки и активити развязаны биндингом; для событий подписчик знает схему
события (своя копия типа, дескриптор из манифеста или динамически) —
пакет эмиттера ему не нужен.

Что даёт backplane, чего нет в голом стеке:

1. **Развязка** — хуки, активити, биндинги, правила (§7, §8).
2. **Одно место** — карточка каждого сервиса и его UI в одной консоли (§11).
3. **Конфиг с историей** — форма (если есть схема), валидация, ревизии,
   откат над Consul KV (§5).
4. **Каталог** — всё объявленное всеми сервисами, из манифестов (§4).

| компонент | владеет | backplane |
|---|---|---|
| Consul | каталог живых инстансов, KV | читает каталог, манифесты, состояние инстансов; пишет `config/*` |
| NATS | события | наблюдает каталог, lag, DLQ; держит consumers правил |
| Temporal | исполнение: workflows сервисов, активити, биндинги | Nexus-handler и worker биндингов; консоль запусков |
| PostgreSQL | хранилище backplane | биндинги, правила, ревизии, сессии, аудит |
| SDK | один способ написать сервис | контракт |

Правило выбора механизма:

- нужно, чтобы кто-то сделал X, и нужен ответ → **хук** + биндинг (§7);
- произошёл факт, кому интересно — пусть реагируют → **событие**, при
  необходимости с **правилом** (§8);
- чужой сервис, не наш модуль → прямой вызов через Consul DNS (§10).

Принципы:

- **Мы не деплоим.** Кто, где и как запускает процесс — не наше дело.
  Ничего не требует k8s; всё работает в compose и на VM.
- **Внешнюю публикацию задаёт деплой, внутреннее API UI-модулей обслуживает консольный relay** (§11) —
  проброс трафика, адрес из Consul, без состояния. Конфиг — через Consul
  KV, хуки и активити — через очереди Temporal; туда по адресу не ходит
  никто. Чтение телеметрии сервером модуля — через защищённое API
  backplane (§15); драйвер обращается к хранилищу.
- **Сервис не зависит от backplane до последнего момента.** Без backplane
  сервис запускается, конфигурируется из env/файла и работает.
- **Платформа требует имён, не схем.** Хук, активити, событие, ключи
  конфига должны быть объявлены — иначе нечего биндить и показывать.
  Схема (schemapb; protobuf — удобный способ её получить, не обязанность)
  — приложение к манифесту: есть — формы, валидация CEL при сохранении,
  подсветка, маскирование секретов; нет — JSON-редактор и ошибки в
  рантайме. SDK снимает схему автоматически с proto и Go-структур: для
  конфигурации схема, которая не снимается, — ошибка `Open`, для хуков, активити,
  событий и workflows — объявление без схемы (payload — просто JSON).
- **Два уровня на проводе.** Протокол платформы — protobuf binary, наши
  пакеты `backplane.*`: манифест, состояние инстанса, envelope хука и
  активити, API консоли. Внутри envelope — payload автора как непрозрачный
  JSON: платформа его не типизирует (схемы нет), CEL читает его как `dyn`.
  Внутреннее и внешнее API сервиса платформа не трогает и не декодирует.
- **Язык выражений биндингов и правил — CEL.** Запросы к телеметрии
  используют языки драйвера (§15).

## 1. Имя

**backplane** — объединительная плата (PCB backplane): сервисы вставляются,
она даёт шину и разъёмы, но не решает, что в неё вставят. Go-модуль
`github.com/gopherex/backplane`, proto-пакеты `backplane.*`, env-префикс
`BACKPLANE_`.

## 2. Стек

Все компоненты — внешние процессы, backplane их не запускает.

| | роль | обязателен |
|---|---|---|
| **Consul** | реестр сервисов (каталог, health), KV (конфиг, манифесты, состояние инстансов), DNS | да |
| **NATS JetStream** | события | нет |
| **Temporal** | workflows сервисов, исполнение хуков, биндингов и правил | нет |
| **PostgreSQL** | собственное состояние backplane | да |
| **Valkey** | общее короткоживущее состояние реплик backplane (попытки входа в консоль) | да |

«Обязателен» — для установки с backplane. Сервис на SDK стартует и
работает без любого из них: недостающее — предупреждение в лог, не ошибка
(§4.5).

Consul — не Kubernetes-специфичен, ставится везде; это и делает возможным
«мы не деплоим». Service mesh не требуется (§10).

## 3. Сервис и его манифест

Сервис — процесс на нашем SDK, со своим API, своими портами, своим кодом.
Всё, что он хочет от платформы, он **объявляет**; SDK собирает объявления
в **манифест** (`backplane.v1.Manifest`, §4.2) в `Run`. Восемь разделов:

| # | раздел | кто потребляет | обязательно | схема (опционально) |
|---|---|---|---|---|
| 0 | конфигурация: обычные и Live-поля | консоль, Consul KV | ключи | schemapb из Go-структуры |
| 1 | внешнее API | консоль (информация) | описания API | OpenAPI или proto-дескрипторы |
| 2 | внутреннее API | его UI-бандл через консольный прокси | полные имена gRPC-сервисов | — (relay байтов) |
| 3 | события, которые он отправляет, и реакторы на чужие | подписчики, консоль | имена | schemapb |
| 4 | хуки, которые он вызывает | backplane (биндинги) | имена | schemapb входа/выхода |
| 5 | активити, которые он может обработать | биндинги и правила | имена | schemapb входа/выхода |
| 6 | UI-бандл | консоль | `plugin.json` | — |
| 7 | свои workflows (§9); расписания — административные | консоль | имена | schemapb входа/выхода |

Плюс то, что SDK добавляет сам: дерево узлов автора (`nodes`) и общий
`FileDescriptorSet` сервисов (`descriptors`).

Объявления — в коде, с любыми Go-типами. Генератор
`protoc-gen-backplane` (поверх `protoc-gen-go`, `protoc-gen-go-temporal`),
который делал бы объявления и типизированные обёртки из proto, — удобство
на будущее: его нет, и SDK от него не зависит.

### 3.0 Конфигурация

Одна структура автора, одна схема. Блок SDK (`config.Backplane`) — её
часть. Поля бывают трёх видов:

- **обычные** — за них отвечает деплой: адреса, порты, креды. Источники —
  env и файл; backplane их не меняет, консоль показывает read-only;
- **`config.Live[T]`** — операционные ручки: фичи, лимиты, уровень логов.
  Меняются из консоли; Consul KV переопределяет **только** их и применяет
  на месте, без рестарта. `Get()` — текущее значение; `Watch(fn)` —
  колбэк на каждое новое значение после загрузки (по порядку, в горутине
  обновления конфигурации; паника колбэка логируется и не роняет сервис);
  `config.LiveOf(v)` — Live со значением для дефолтов в коде и тестов;
  нулевой Live читает нулевое значение и никогда не срабатывает. Все копии
  секции делят одно текущее значение. JSON (`MarshalJSON`) — текущее
  значение. Live-поля живут только в структурах: Live внутри слайса,
  массива или map — ошибка `Open`. Схема помечает их аннотацией
  `backplane.live`, манифест перечисляет пути (`config.live`,
  `greeter.suffix`);
- **`config.Secret`** — маскируется везде: `***` для любого fmt-глагола
  (`%v`, `%s`, `%d`, `%q`, `%x`, `%+v`, `%#v`), `MarshalText` (slog, YAML),
  JSON, эффективная конфигурация в состоянии инстанса, схема (`secret`).
  Значение — `Reveal()`. fmt не видит Secret в неэкспортированных полях
  при печати структуры через `%v`: такие поля экспортируются или структура
  не печатается.

Подключение к базе и уровень её логирования живут в одной секции: первое
— обычное поле, второе — `Live`. Секция передаётся компоненту целиком
(указателем на секцию из `Root.Config()`), и он сам читает живые поля.

Проверки сверх схемы — метод `Validate() error` на конфигурации или
любой секции (`config.Validator`): ошибка — отказ `Open`, а для
обновления из KV — отказ в его применении целиком (§5.5).

Пакет `config` работает и без остального SDK: `config.Load[C](ctx, opts...)`
читает один раз (defaults, файл, env) — для утилит и тестов;
`config.Open[C](ctx, opts...)` (C встраивает `config.Backplane`)
возвращает `*config.Runtime[C]` — живую конфигурацию: `Value()`
(стабильный указатель, Live-поля обновляются на месте), `Degraded()`
(почему слой Consul пуст или устарел), `Close()`. Опции: `Service`
(имя, если `build.Service` не проштампован), `File` (вместо
`BACKPLANE_CONFIG_FILE`; YAML или JSON по расширению), `WithoutFile`,
`EnvPrefix` (вместо `<SERVICE>_`), `WithoutEnv`, `Source` (свои слои выше
env, ниже KV), `WithoutConsul`, `consul.Config(opts...)` из `drivers/consul` (в Consul-источник xconf),
`ConsulBackoff(min, max)` (пауза между попытками до недоступного Consul,
по умолчанию 1s..30s). `ctx` ограничивает только загрузку; runtime живёт
до `Close`.

Env автора — `<SERVICE>_<PATH>`: префикс — имя сервиса в верхнем
регистре, `-` и `.` → `_` (`my-svc` → `MY_SVC_`); путь — JSON-имена
полей в верхнем регистре через `_` (`HELLO_GREETER_SUFFIX`). Блок SDK
читается из фиксированного `BACKPLANE_<PATH>` (§4.3); `<SERVICE>_*` —
слой выше, так что `<SERVICE>_BACKPLANE_<PATH>` перекрывает
`BACKPLANE_<PATH>`. Объект или список задаётся одной переменной целиком
в JSON (`BACKPLANE_CONSUL_TAGS='["blue"]'`); объект целиком и его поле по
отдельности вместе — ошибка загрузки.

`config.TLS` — TLS клиентского соединения: `Enabled`, `CA`, `Cert`, `Key`
(`Secret`), `ServerName`, `InsecureSkipVerify`; PEM-содержимое, не пути.
`ClientConfig()` строит из него `*tls.Config` (TLS 1.2+; `nil` при
выключенном TLS; пустой `CA` — системный пул; `Cert` и `Key` — только
вместе, иначе `config.ErrTLSPair`; `CA` без сертификата —
`config.ErrTLSCA`) — это тот же `*tls.Config`, что SDK строит для
Consul, NATS и Temporal.

### 3.1 Внешнее API

Автор сам выбирает протоколы, обработчики, пути, порты и преобразования.
SDK умеет обслуживать HTTP, native gRPC, ws-proto и авторский GraphQL handler.
Декларации `Manifest.apis` (`backplane.v1.API`) описывают API для консоли;
они не публикуют сервис и не настраивают внешнюю маршрутизацию.

```go
import "github.com/gopherex/backplane/pkg/backplane/api"

svc.GRPC(st.Greeter.Register, api.Reflection())
svc.HTTP("/hello/", handler, api.OpenAPIFS(files, "openapi.yaml"))
wsproto.Serve(svc, "/ws/", api.Origins{}, st.Greeter.Register)
svc.GraphQL("/graphql/", graphqlHandler, introspection)
// Собственный сервер автора уже обслуживает этот API:
svc.API(api.HTTP("/legacy/", api.Port(8083), api.OpenAPI(document)))
```

`API` содержит `{prefix, host, kind, port, services, schema}`. Схемы:
OpenAPI, FileDescriptorSet или GraphQL introspection; большие многофайловые
документы доставляются через immutable bundle на platform-порту.
Внешних URL платформа не задаёт. Port и prefix описывают собственный listener сервиса.

`api.Listen` выбирает managed listener, `api.Host` ограничивает HTTP handler по Host.
HTTP prefix нормализуется с завершающим `/`; одинаковые host/prefix/port — ошибка
декларации. Interceptors и middleware выполняются внутри сервиса.
`BACKPLANE_SERVER_*` задаёт реальные ограничения listener'а, включая HTTP headers,
idle timeout и максимальный размер gRPC сообщения. Прикладные deadline, CORS,
ограничения тела и преобразования протоколов автор реализует кодом либо в деплое.
В SDK нет декларативной политики внешнего прокси или автоматического транскодирования.

### 3.2 Внутреннее API

Обычный gRPC-сервис на **платформенном порту** сервиса (§4) — для его
собственного UI-бандла. Наружу не публикуется; в него ходит только
консольный прокси backplane (§11), байты как есть. Другим сервисам
недоступно. Регистрация — `svc.Internal(register)`. Proto-пакет —
`<service>.console.v1` (`-` в имени сервиса → `_`; `internal` в пути
запрещён Go): сервис из другого пакета — ошибка `Run`. **[backplane]**
Полные имена методов должны быть уникальны в установке: манифест с
коллизией backplane отвергает.

Внутреннее API закрыто секретом (`BACKPLANE_INTERNAL_SECRET`, §11.1) и
обслуживается только пока поднято дерево автора: до его старта (например,
пока обязательная зависимость ещё ретраится) вызовы получают
`UNAVAILABLE`, а на остановке SDK закрывает приём и ждёт вызовов в полёте
до того, как дерево и его зависимости остановятся. `grpc.health.v1` на
том же сервере не закрыт ни секретом, ни этим гейтом.

### 3.3 События

Именованные сообщения, которые сервис публикует в NATS как CloudEvents с
JSON-payload (§8). `event.Declare[T](scope, "Greeted", event.Describe(s))`
возвращает `event.Ref[T]`; `ref.Publish(ctx, v, opts...)` отправляет
(опции — `event.Key`, `ID`, `Time`, `Header`, §8), `ref.Name()` — полное
имя `<service>.Greeted`. Реактор на событие любого сервиса —
`event.React[T](scope, "iam.UserRegistered", fn, opts...)` (опции
доставки — `event.MaxDeliver`, `Concurrency`, `Ordered`, `Timeout`,
`Redelivery`, `StartAt`, `Consumer`, `InactiveThreshold`, §8); в манифесте
он — `subscriptions` (`event` и `consumer`). Метаданные доставки —
`event.DeliveryOf(ctx)`, ошибка без повторов — `event.Terminal(err)`,
повторная обработка dead letters — `event.Redrive`, нативный клиент —
`natsdriver.JetStream(scope)`.

### 3.4 Хуки («нужно»)

Операции, которые сервис **вызывает**, не имея реализации:
`iam.SendEmail(to, template, data) → {message_id}`. Кто и как ответит —
биндинг в backplane (§7).

```go
send := hook.Declare[Email, Sent](root, "SendEmail", hook.Required(),
    hook.DefaultTimeout(10*time.Second), hook.Describe("письмо пользователю"))

out, err := send.Call(ctx, in, hook.Key(in.RequestID), hook.Timeout(5*time.Second))
```

- `hook.Declare[Req, Res](scope, name, opts...)` возвращает
  `hook.Ref[Req, Res]`. Опции: `hook.Required()` — без биндинга сервис
  работать не может; `hook.DefaultTimeout(d)` — дедлайн вызова, если сам
  вызов его не задал (`d ≤ 0` игнорируется); `hook.Describe(s)` —
  описание для консоли. Манифест несёт `required`, `timeout`,
  `description` и схемы. `ref.Name()` — полное имя `<service>.<Name>`.
- `ref.Call(ctx, req, opts...)` работает из любого Go-кода — HTTP-handler'а,
  реактора, активити, `OnStart` и `OnStop` компонента (§7.2); из
  workflow-кода — `workflows.CallHook(wctx, ref, req, opts...)` с
  `workflow.Context`. Пока транспорта нет (Temporal не задан, не
  подключён или backplane ещё не завёл Nexus endpoint сервиса), `Call`
  сразу возвращает `hook.ErrUnavailable`; нет биндинга —
  `hook.ErrNoBinding`.
- Дедлайн вызова — самый ранний из дедлайна `ctx` и собственного:
  `hook.Timeout(d)` вызова, иначе `DefaultTimeout` объявления, иначе — если
  у `ctx` дедлайна нет — платформенный `backplane.temporal.hook_timeout`
  (30 s, §4.3). У `CallHook` дедлайн есть всегда: тот же выбор, урезанный
  до остатка таймаута run'а; бесконечного вызова хука не бывает.
- `hook.Key(k)` — идемпотентный вызов: вызовы с одним ключом исполняют
  биндинг один раз. Вызов, пока первый идёт, ждёт его; вызов после
  успешного — получает его результат, не исполняя заново (вход повторного
  вызова не смотрится); вызов после неуспешного — исполняется снова. Ключ
  живёт, пока Temporal хранит завершённый запуск (retention namespace'а).
  `CallHook` ключ игнорирует — workflow и так durable.
- Метрика `backplane.hook.call.duration` (атрибуты `hook`, `outcome`:
  `ok`, `no_binding`, `unavailable`, `timeout`, `error`) — на каждый `Call`
  (`CallHook` виден в истории workflow и её не пишет).

### 3.5 Активити («умею»)

Операции, которые сервис **реализует** и которые могут быть целями чужих
биндингов и правил: `template.Exec(name, data) → {subject, text}`,
`smtp.Send(to, subject, text) → {id}`. Temporal activities или workflows на
очереди сервиса; вход и выход — JSON, SDK декодирует в тип автора.
Объявление вместе с реализацией:

```go
activity.Handle(root, "Send", smtp.Send, activity.StartToClose(10*time.Second),
    activity.HeartbeatTimeout(5*time.Second), activity.Retry(activity.RetryHint{Attempts: 5}),
    activity.Describe("отправка письма"))
workflows.Activity(root, "Onboard", onboarding.Run) // func(workflow.Context, Req) (Res, error)
```

- `activity.Handle(scope, name, func(ctx, in Req) (Res, error), opts...)` —
  Temporal activity (`kind: ACTIVITY` в манифесте). Ошибка обработчика
  повторяется по политике шага; `activity.NonRetryable(err)` — без
  повторов; вход, который не декодируется, — тоже без повторов (§7.2).
- `workflows.Activity(scope, name, func(wctx, in Req) (Res, error), opts...)`
  — активити на workflow (`kind: WORKFLOW`): шаг биндинга исполняет её как
  child workflow с этим именем на очереди сервиса; внутри можно ждать,
  спать, вызывать хуки (`CallHook`) и свои активити. Ошибки — как у
  `Handle` (§7.2).
- Опции — умолчания для шага биндинга, который её вызывает (биндинг может
  переопределить; SDK их не навязывает): `StartToClose(d)` — одна попытка
  (для `Workflow` — один run), `HeartbeatTimeout(d)` — попытка без
  heartbeat дольше `d` считается потерянной, `Retry(RetryHint{Attempts})` —
  сколько попыток всего, `Describe(s)` — описание. В манифесте —
  `start_to_close`, `heartbeat`, `retry.attempts`, `description`.
- `activity.InfoOf(ctx) (Info, bool)` в обработчике: `Attempt` (1 —
  первая), `Binding` и `Step` (из `ActivityCall`: кто вызвал и какой шаг),
  `Key` — `<workflow id>/<activity id>`, одинаковый у всех попыток одного
  исполнения шага (ключ идемпотентности для внешних побочных эффектов),
  `Deadline` попытки. Обработчик, вызванный напрямую, а не транспортом или
  `backplanetest.Activity` (§4.6), получает `false`.
- `activity.Heartbeat(ctx, details...)` — прогресс долгого обработчика:
  таймаут heartbeat отсчитывается от последнего, отмену шага `ctx`
  замечает на следующем; вне транспорта ничего не делает.

Хуки, события, реакторы и активити объявляются на любом узле дерева
(`deps.Scope`: `Root`, компонент) до `Run`: объявление находит свой сервис
через узел, глобального реестра нет. Имена хуков, активити и workflows
(§9) — CamelCase, `[A-Z][A-Za-z0-9]*` (`SendEmail`); другое имя — паника
при объявлении с понятным сообщением: это имена типов Temporal и операций
Nexus. Схемы входа и выхода снимаются с Go-типов (schemapb); не снялась —
объявление остаётся, payload — просто JSON (protojson для
proto-сообщений). Вход и выход меняются только аддитивно: читатель
игнорирует незнакомые поля, отсутствующее поле читается нулевым значением,
переименование поля — это новое поле рядом со старым, пока все читатели не
перейдут. Имя на проводе — `<service>.<Name>`.

### 3.6 UI-бандл

Module Federation remote, собранный против UI SDK; `plugin.json` с
`sdk_major` и навигацией. Работает с внутренним API (§3.2) через консоль.
Объявление — `svc.UI(bundle fs.FS)`; манифест получает `ui.hash` (sha256
по пути и содержимому каждого файла бандла) и `ui.sdk_major` из
`plugin.json`; бандл без читаемого `plugin.json` — ошибка `Run`. SDK
отдаёт бандл на платформенном порту (`GET /_backplane/ui/<path>`, за
секретом, §4.1) с `ETag` = `"<ui.hash>"` и `Cache-Control: no-cache` на
каждом файле: кэш перепроверяет файл и получает `304`, пока бандл не
изменился. Доставка в браузер — §11.2.

## 4. Регистрация, порты, манифест

### 4.1 Порты

У сервиса два класса портов:

- **платформенный порт** — один (`BACKPLANE_INTERNAL_PORT`, 9400),
  поднимает SDK, всё платформенное через cmux: внутреннее API (gRPC),
  `grpc.health.v1`, HTTP-пробы xprobe (`/healthz/liveness`,
  `/healthz/readiness`, `/healthz/startup`), `GET /_backplane/ui/*` (бандл,
  §3.6), `GET /_backplane/info` (JSON: `service`, `version`, `instance`,
  `advertise`, `environment`, `sdk_version`, `go_version`, `commit`,
  `build_date`) и, с `BACKPLANE_PPROF=true`, `/debug/pprof/*`. Пробы и
  `grpc.health.v1` открыты; остальное — только с секретом (§11.1). Через
  внешнюю инфраструктуру не публикуется;
- **порты внешнего API** — общий публичный порт managed-роутов
  (`BACKPLANE_PUBLIC_PORT`, 8080), отдельные `api.Listen(addr)` и порты,
  которые автор обслуживает сам (declarative-роуты).

Пробы автора — `svc.LivenessProbe(p)`, `svc.ReadinessProbe(p)`,
`svc.StartupProbe(p)` (xprobe). Обязательные зависимости входят в
readiness сами; в liveness зависимостям не место — мёртвая база не повод
перезапускать процесс.

### 4.2 Consul

Одно место для всего зарегистрированного:

- **Каталог** — кто жив и где. Регистрация: ID = id инстанса
  (`BACKPLANE_INSTANCE`, иначе `<service>-<hostname>`),
  адрес = `BACKPLANE_ADVERTISE`, иначе `POD_IP`, иначе IP hostname'а;
  `Port` = порт внешнего API (без порта, если внешнего API нет); `Tags` —
  `BACKPLANE_CONSUL_TAGS`. Health check — gRPC health на платформенном
  порту: интервал `_CHECK_INTERVAL` (10s), таймаут `_CHECK_TIMEOUT` (5s),
  снятие после `_DEREGISTER_AFTER` (1m) в critical. В каталог инстанс
  попадает только когда обслуживает трафик (узел `register`, §4.4) и
  уходит из него первым на остановке; пока зависимости ретраятся, он виден
  только по состоянию (фаза `starting`). Регистрирует SDK (compose,
  VM) или деплой (consul-k8s, Nomad) — тогда
  `BACKPLANE_CONSUL_REGISTER=false`, а свой Consul service ID SDK берёт из
  `BACKPLANE_INSTANCE`. Адрес и `Port` описывают регистрацию сервиса. Адрес по
  умолчанию — первый global unicast IPv4 hostname'а, иначе IPv6, иначе
  интерфейсов; если остаётся loopback, а SDK регистрирует инстанс, старт
  пишет предупреждение: такой адрес недостижим с других хостов.
- **Манифест** — `backplane/services/<name>/manifests/<version>`,
  `backplane.Manifest` proto binary до 512 KB, по ключу на версию сервиса. Пишет SDK при старте:
  отсутствующий ключ создаётся, совпадающий не трогается. Другой манифест
  под той же выпущенной версией SDK **не перезаписывает**: ключ остаётся
  прежним, в лог — ошибка (версия называет один набор объявлений — выпустите
  новую), присутствие в Consul продолжается. Версию с build-метаданными
  (`+…`, dev-сборки) SDK перезаписывает. Версия без штампа (`0.0.0`) записывается как
  `0.0.0+<12 hex хэша манифеста>`: dev-сборки с разными объявлениями не
  перетирают манифесты друг друга. Отсутствующий ключ создаётся CAS'ом:
  два инстанса одной версии, стартующие одновременно, не спорят.
  Содержимое: `service`, `version`, `sdk_version`; `config` (схема, ключи
  верхнего уровня, пути Live-полей); `routes`; `internal_services`;
  `events`, `hooks`, `activities`; `subscriptions` (реакторы: событие и
  consumer); `workflows` (§9); legacy `schedules` читается только у старых версий; `ui`; `nodes` — дерево автора
  в порядке старта (путь, вид, `optional`); `descriptors` — один
  `FileDescriptorSet` на все сервисы managed-роутов и внутреннего API.
  Манифест больше 512 KB — ошибка `Run`. **[backplane]** Консоль и
  backplane используют манифест **старшей живой версии** (версии живых
  инстансов — из их состояния); ключи версий, у которых не осталось
  инстансов, backplane удаляет. Rolling upgrade и откат не моргают.
- **Состояние инстанса** — `backplane/services/<name>/instances/<id>`,
  `backplane.InstanceState`:
  - `id`, `service`, `version`, `address`, `platform_port`, `started_at`,
    `sdk_version`, `commit` (`build.Commit`, иначе VCS-ревизия сборки);
  - `phase`: `starting` — дерево автора стартует, зависимости
    предоставляются; `serving` — инстанс принимает трафик и (если SDK
    регистрирует) стоит в каталоге; `stopping` — остановка началась,
    регистрации в каталоге уже нет;
  - `config` — эффективная конфигурация (JSON, секреты маскированы),
    `sources` — источник каждого пути (default / файл / env / KV);
  - `config_revision` — `_revision`, прочитанная **тем же запросом**, что и
    применённые значения (0 — ключа нет); `config_error` и
    `config_rejected_revision` — почему и какая ревизия не применена (§5.5);
    пусто и 0, когда последнее обновление применено;
  - `nodes` — зависимости дерева автора: путь, готова ли, ошибка;
  - `transports` — `consul`, `nats`, `temporal`, `otlp`: подключён ли,
    ошибка.

  Состояние пишется с узла `consul` **до** старта дерева автора — инстанс,
  чьи зависимости ещё ретраятся, виден с фазой `starting`; дальше — при
  каждом изменении конфигурации и фазы, а `nodes` и `transports`
  сверяются на каждом продлении сессии (TTL/3). Ключ привязан к
  Consul-сессии инстанса с TTL `BACKPLANE_CONSUL_SESSION_TTL` (30s;
  значение вне допустимых Consul 10s..24h приводится к границе) —
  умирает вместе с ним; сессия продлевается каждые TTL/3, продления,
  неудачные дольше TTL/2, пересоздают присутствие. Сессия называется
  `<instance>#<инкарнация>` (случайная на процесс): первое установление
  присутствия процесса перехватывает ключ у сессии прошлой инкарнации
  того же инстанса (упавший предшественник); сессия другой живой
  инкарнации позже — это дубликат id инстанса: ошибка в лог
  (`duplicate instance id`) и повтор с backoff. **[backplane]** Отсюда
  консоль берёт «что реально применено», backplane — базу для валидации
  override, консольный прокси — адрес и порт для relay.

**[backplane]** backplane держит blocking queries на каталог и на префикс
`backplane/services/`. `Meta` регистрации (512 символов на значение) SDK
не заполняет — всё наше лежит в KV, и внешняя регистрация деплоем ничего
про нас знать не должна.

### 4.3 Блок SDK в конфигурации

Блок `config.Backplane`, встроенный в конфигурацию автора (тег
`json:"backplane"`; `backplane.Open` требует его — `config.Backplaner`).
Env блока фиксирован для всех сервисов: `BACKPLANE_<PATH>`, путь —
JSON-имена полей блока в верхнем регистре через `_`; остальная
конфигурация читается из `<SERVICE>_*` (`HELLO_GREETER_SUFFIX`, §3.0). В
файле конфигурации блок — секция `backplane:`. Таблица — все поля блока
с умолчаниями:

| | |
|---|---|
| `BACKPLANE_CONSUL_ADDR`, `_CONSUL_TOKEN` | реестр и KV; пусто — без Consul |
| `BACKPLANE_CONSUL_DATACENTER` | датацентр запросов; пусто — датацентр агента |
| `BACKPLANE_CONSUL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | HTTPS к Consul: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат, только парой; непарсящийся PEM — ошибка `Open`. Включённый TLS блока целиком заменяет TLS из `CONSUL_*` |
| `BACKPLANE_CONSUL_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат Consul — только для разработки |
| `CONSUL_HTTP_TOKEN`, `CONSUL_HTTP_TOKEN_FILE`, `CONSUL_HTTP_SSL`, `CONSUL_CACERT`, `CONSUL_CAPATH`, `CONSUL_CLIENT_CERT`, `CONSUL_CLIENT_KEY`, `CONSUL_TLS_SERVER_NAME`, `CONSUL_HTTP_SSL_VERIFY` | стандартные переменные клиента Consul: заполняют то, что блок оставил пустым (токен, TLS при выключенном TLS блока); Consul включает только `BACKPLANE_CONSUL_ADDR` |
| `BACKPLANE_CONSUL_REGISTER` | регистрация в каталоге (`true`); `false` — регистрирует деплой |
| `BACKPLANE_CONSUL_TAGS` | теги регистрации в каталоге, JSON-список (`["blue","canary"]`) |
| `BACKPLANE_CONSUL_CHECK_INTERVAL`, `_CHECK_TIMEOUT`, `_DEREGISTER_AFTER` | health check регистрации: интервал (`10s`), таймаут (`5s`), снятие из каталога после стольких в critical (`1m`) |
| `BACKPLANE_CONSUL_SESSION_TTL` | TTL сессии, под которой живёт состояние инстанса (`30s`; вне 10s..24h приводится к границе): продление каждые TTL/3, после TTL/2 неудачных продлений присутствие пересоздаётся; после смерти процесса состояние исчезает не позже чем через TTL |
| `BACKPLANE_INSTANCE`, `BACKPLANE_ADVERTISE` | id инстанса (по умолчанию `<service>-<hostname>`) и адрес (по умолчанию `POD_IP`, иначе адрес hostname'а, §4.2); опции `backplane.Instance` / `Advertise` — выше них |
| `BACKPLANE_INTERNAL_PORT` | платформенный порт (`9400`, 1..65535) |
| `BACKPLANE_INTERNAL_SECRET` | секрет платформенного порта (§11.1); пусто — проверка выключена, `Run` пишет предупреждение |
| `BACKPLANE_INTERNAL_SECRET_PREVIOUS` | прежний секрет, принимаемый наравне с текущим, пока идёт ротация (§13); без `_INTERNAL_SECRET` не действует |
| `BACKPLANE_PUBLIC_PORT` | публичный порт managed-роутов (`8080`, 1..65535) |
| `BACKPLANE_NATS_URL`, `_NATS_CREDS` | события; пусто — без NATS; `_NATS_CREDS` — содержимое `.creds`-файла |
| `BACKPLANE_NATS_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | TLS к NATS: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат, только парой; непарсящийся PEM — ошибка старта узла `nats` |
| `BACKPLANE_NATS_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат NATS — только для разработки |
| `BACKPLANE_NATS_PUBLISH_TIMEOUT` | предел `Publish`, чей `ctx` без дедлайна (`5s`) |
| `BACKPLANE_NATS_MAX_AGE`, `_NATS_MAX_BYTES` | хранение собственного стрима событий `bp_<service>`: возраст (`168h`) и размер в байтах (`0`); `0` — без ограничения (§8) |
| `BACKPLANE_NATS_REPLICAS` | реплики стрима событий и стрима dead letters сервиса (`1`, от 1 до 5) |
| `BACKPLANE_NATS_DEDUP_WINDOW` | окно дедупликации стрима событий (`2m`); `0 < окно ≤ max_age`, иначе ошибка `Open` |
| `BACKPLANE_NATS_DLQ_MAX_AGE` | хранение dead letters `bp_dlq_<service>` (`720h`); `0` — без ограничения |
| `BACKPLANE_TEMPORAL_ADDR`, `_TEMPORAL_NS` | хуки, активити, workflows; пустой адрес — без Temporal; namespace (`default`; поле `ns`: `namespace` — зарезервированное слово CEL) |
| `BACKPLANE_TEMPORAL_TLS_ENABLED`, `_TLS_CA`, `_TLS_CERT`, `_TLS_KEY`, `_TLS_SERVER_NAME` | TLS к Temporal: содержимое PEM, не пути; пустой `_TLS_CA` — системный пул; `_TLS_CERT` + `_TLS_KEY` — клиентский сертификат (mTLS), только парой; непарсящийся PEM — ошибка старта узла `temporal` |
| `BACKPLANE_TEMPORAL_TLS_INSECURE_SKIP_VERIFY` | не проверять сертификат сервера — только для разработки |
| `BACKPLANE_TEMPORAL_API_KEY` | API key Temporal Cloud (`Authorization: Bearer`); включает TLS и без `_TLS_ENABLED` |
| `BACKPLANE_TEMPORAL_DIAL_TIMEOUT` | одна попытка подключения (`2s`): первая — на старте узла `temporal`, следующие — в фоне; он же таймаут проверки соединения |
| `BACKPLANE_TEMPORAL_HOOK_TIMEOUT` | дедлайн вызова хука, если его не задали ни `ctx`, ни вызов, ни объявление (`30s`, §3.4) |
| `BACKPLANE_TEMPORAL_WORKER_ENABLED` | `false` — реплика без worker'а активити и workflows; worker вызовов хуков работает и здесь (§9) |
| `BACKPLANE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITIES`, `_MAX_CONCURRENT_WORKFLOW_TASKS` | параллелизм worker'а (`0` — умолчание Temporal; отрицательное — ошибка `Open`) |
| `BACKPLANE_TEMPORAL_WORKER_ACTIVITY_POLLERS`, `_WORKFLOW_POLLERS` | поллеры worker'а (`0` — умолчание Temporal; отрицательное — ошибка `Open`) |
| `BACKPLANE_ENVIRONMENT` | `deployment.environment.name` телеметрии и `environment` в `/_backplane/info`; пусто — атрибута нет |
| `BACKPLANE_LOG_LEVEL` | уровень лога (`info`); Live — меняется из консоли на лету, непарсящееся значение оставляет прежний уровень с предупреждением в лог. С `backplane.Logger(l)` не применяется: уровень, выводы и OTLP-tee переданного логгера — дело автора |
| `BACKPLANE_SHUTDOWN_TIMEOUT` | бюджет всей остановки (`25s`); `terminationGracePeriodSeconds` — больше него |
| `BACKPLANE_SHUTDOWN_DRAIN` | пауза между дерегистрацией и закрытием listener'ов, чтобы балансировщики заметили уход (`3s`); `0 ≤ drain < timeout` |
| `BACKPLANE_SHUTDOWN_LISTENERS` | сколько публичные listener'ы и внутреннее API дорабатывают запросы в полёте (`10s`, больше 0); потом стримы обрываются, а контексты внутренних вызовов отменяются |
| `BACKPLANE_SHUTDOWN_RESERVE` | запас бюджета на дерево автора, зависимости и телеметрию после listener'ов (`5s`, не меньше 0); `drain + listeners + reserve ≤ timeout`, иначе ошибка `Open` — короткий `timeout` требует коротких этапов |
| `BACKPLANE_HEALTH_INTERVAL`, `_HEALTH_TIMEOUT` | пробы (readiness, liveness, startup) пересчитываются раз в интервал (`5s`), одна проверка — не дольше таймаута (`3s`); `0 < timeout ≤ interval`, иначе ошибка `Open`. Тем же интервалом `deps.ProbeOptional` проверяет опциональные зависимости |
| `BACKPLANE_SERVER_READ_HEADER_TIMEOUT` | HTTP: чтение заголовков запроса (`10s`); он же — сколько новое соединение может молчать, пока cmux определяет протокол |
| `BACKPLANE_SERVER_IDLE_TIMEOUT` | HTTP: простой keep-alive соединения (`2m`) |
| `BACKPLANE_SERVER_MAX_HEADER_BYTES` | HTTP: предел заголовков запроса (`1048576`); больше — `431` |
| `BACKPLANE_SERVER_GRPC_MAX_RECV_MSG_SIZE` | gRPC: предел входящего сообщения (`4194304`) |
| `BACKPLANE_SERVER_GRPC_KEEPALIVE_MIN_TIME`, `_GRPC_PERMIT_WITHOUT_STREAM` | gRPC: минимальный интервал keepalive-пингов клиента (`30s`) и пинги на соединении без стримов (`true`) |
| `BACKPLANE_PPROF` | `/debug/pprof/*` на платформенном порту за секретом (`false`) |
| `BACKPLANE_CONFIG_FILE` | файл конфигурации (YAML по `.yaml`/`.yml`, иначе JSON) той же формы, что структура; слой ниже env. Не поле блока: читается до загрузки |

Телеметрия — стандартные `OTEL_*` (xtrace `contrib/sdk`); сигнал без
endpoint (общего `OTEL_EXPORTER_OTLP_ENDPOINT` или своего
`OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT`) не экспортируется, одна запись в лог.

### 4.4 SDK изнутри

Автор описывает сервис двумя структурами и одним конструктором:

```go
type Config struct {
    config.Backplane `json:"backplane"`
    Greeter greeter.Config `json:"greeter"`
    Store   store.Config   `json:"store"`
    Cache   store.Config   `json:"cache"`
}

type State struct {
    Store   deps.Dependency[*store.DB]
    Cache   deps.Optional[*store.DB]
    Greeter *greeter.Greeter
}

func NewState(root backplane.Root[Config]) (*State, error) {
    cfg := root.Config() // копия; Live-поля общие с живой конфигурацией
    st := &State{
        Store: deps.NewDependency(root, store.New(&cfg.Store)),
        Cache: deps.NewOptional(root, store.New(&cfg.Cache), deps.Name("cache")),
    }
    g, err := greeter.New(root, &cfg.Greeter, st.Store) // всё — аргументами
    ...
}

svc, err := backplane.Open(ctx, NewState) // конфиг → Root → NewState
svc.GRPC(svc.State().Greeter.Register)
err = svc.Run(ctx)
```

**Open и Run.** `backplane.Open(ctx, newState, opts...)` загружает
конфигурацию, проверяет блок SDK, строит State конструктором и возвращает
`*backplane.Service[State]`; ничего не слушает. `ctx` ограничивает только
загрузку (обязательное Live-поле может ждать Consul, §5.3) — дальше
сервис от него не зависит. Невалидная конфигурация и ошибка конструктора —
ошибка `Open`, взятое к этому моменту освобождается. Между `Open` и `Run`
автор объявляет роуты, внутреннее API, UI и пробы на `Service`. `Run(ctx)`
запечатывает объявления, поднимает сервис и блокируется до конца `ctx`,
SIGINT/SIGTERM или ошибки горутины узла; затем останавливает всё.
Объявление после `Run` — ошибка программиста, паника. Второй `Run`, как и
`Run` после `Close`, возвращает `backplane.ErrClosed`. `svc.Close()`
освобождает то, что взял `Open` (горутины живой конфигурации), если `Run`
вызываться не будет; повторный `Close` безопасен, `Run` закрывает сам.

Опции `Open`: `backplane.Name(n)`, `Version(v)`, `Instance(id)`,
`Advertise(addr)`, `Logger(l)`, `ConfigOptions(opts...)` (в `config.Open`),
`KeepSlog()`, `GRPCServerOptions(opts...)` (§3.1), `RequireNATS()` и
`RequireTemporal()` — соединение входит в readiness: пока оно не поднято,
инстанс не ready; без настроенного транспорта — ошибка `Open`. Имя
сервиса — `Name`, иначе `build.Service` из ldflags, иначе последний
элемент пути main-пакета (`.../cmd/hello` → `hello`), так что `go run`
работает; без имени или с именем не по `^[a-z][a-z0-9-]*$` (§17) — ошибка
`Open` (`backplane.ErrConfig`). Версия — `Version`,
иначе `build.Version`, иначе версия модуля, иначе `0.0.0+<commit>`, иначе
`0.0.0` (в манифесте — с хэшем, §4.2). По умолчанию `Run` направляет
`log/slog` в логгер сервиса (через него логируют OTel SDK и другие
библиотеки) и восстанавливает прежний default при выходе; `KeepSlog`
оставляет `slog` в покое.

`Service` даёт `State()`, `Name()`, `Identity()` (сервис, версия,
инстанс, адрес, окружение), `Log()` и объявления: `HTTP`, `GRPC`,
`GraphQL`, `Route` (§3.1), `Internal` (§3.2), `UI` (§3.6),
`LivenessProbe`/`ReadinessProbe`/`StartupProbe` (§4.1); ws-proto —
`wsproto.Serve(svc, ...)`.

**Дерево узлов** (`deps`). `Root` — корень; под ним автор создаёт узлы и
передаёт их друг другу аргументами конструкторов. `Root` — сам узел
(`deps.Component`), плюс `Config()` — копия загруженной конфигурации
(обычные поля не меняются, Live-поля общие для всех копий и обновляются на
месте) и `Identity()`. Встраивать `Root` в State не обязательно.

- `deps.Component` + `deps.NewComponent(parent, name)` — код автора;
  встраивается в его тип. Нулевой `Component` никуда не логирует, а
  `Go`/`OnStart`/`OnStop` на нём — паника;
- `deps.Dependency[T]` + `deps.NewDependency(parent, provider, opts...)` —
  обязательный внешний ресурс: его старт ретраится с backoff до успеха
  (сервис жив, но не ready), он входит в readiness своей пробой,
  закрывается на стопе. Трафик — публичный и внутренний — идёт только когда
  готовы все обязательные зависимости, и прекращается до их закрытия, так
  что `Get()` в handler'ах всегда даёт значение; `Ready()`, `Err()`;
- `deps.Optional[T]` + `deps.NewOptional(parent, provider, opts...)` —
  зависимость, без которой сервис работает: старт её не ждёт, provide
  ретраится в фоне до успеха или остановки, в readiness не входит.
  `Get()` возвращает `(T, bool)` — есть ли она сейчас; `Err()` — почему нет.
  С `deps.ProbeOptional()` предоставленная зависимость проверяется своей
  пробой раз в `Health.Interval`: пока проба падает, `Get()` отвечает
  `(zero, false)`, а `Err()` — ошибкой пробы; значение не закрывается и
  возвращается, когда проба проходит;
- `deps.Singleton[T]` + `deps.NewSingleton(parent, factory)` — ленивое
  значение: строится при первом `Get(ctx)` (неудача — повтор при
  следующем), закрывается на стопе, после стопа — `deps.ErrClosed`. В
  readiness не входит; фабрика не создаёт дочерних узлов. `Get` держит
  замок синглтона, пока работает фабрика: параллельные `Get` ждут эту
  одну сборку и получают её значение (при неудаче — пробуют сами по
  очереди), свой `ctx` во время ожидания они не смотрят; стоп ждёт
  идущую сборку и закрывает её результат. Медленную фабрику ограничивает
  `ctx`, который она получает.

Опции зависимостей: `deps.Name(n)` (имя узла вместо имени провайдера —
две базы одного вида), `deps.Backoff(min, max)` (пауза между попытками,
по умолчанию 1s..30s с джиттером), `deps.ProvideTimeout(d)` (одна попытка
`Provide`, 30s; зависшая попытка ретраится как неудачная; `ctx` попытки
заканчивается вместе с ней — провайдер его не хранит),
`deps.ProbeTimeout(d)` (одна проба; по умолчанию — `Health.Timeout` у
обязательной, `Health.Interval` у `ProbeOptional`),
`deps.ProbeOptional()` (только `NewOptional`). Метрики:
`backplane.dependency.provide.attempts{node, outcome}` на каждую попытку и
`backplane.dependency.ready{node}` — 1, пока значение предоставлено (и
проходит пробу при `ProbeOptional`). Для тестов и значений, которыми
сервис владеет сам, — `deps.Static(v)` и `deps.StaticOptional(v)`;
нулевой `Optional` — отсутствующая зависимость. `IsZero()` у каждого вида
отличает несозданный узел: `Get()` нулевой `Dependency` — нулевое
значение без паники, `Err()` нулевой зависимости — `deps.ErrNotReady`.

Провайдер (`deps.Provider[T]`: `Name`, `Provide`, `Probe`, `Close`) — форма
подключений SDK к инфраструктуре (`pkg/backplane/infra/<name>`): секция
конфигурации + `New(Config) deps.Provider[T]`. Ими пользуется и сам
backplane, и автор — для своих ресурсов (своя база, свой кэш, свой
кластер); узел называют `deps.Name`:

```go
type Config struct {
    config.Backplane `json:"backplane"`
    Cache valkey.Config `json:"cache"` // GREETER_CACHE_ADDR, _PASSWORD, ...
}

cache := deps.NewDependency(root, valkey.New(cfg.Cache), deps.Name("cache"))
```

Пакеты: `infra/postgres` (pgxpool с otelpgx и логом запросов без
аргументов, pgtx, схема и миграции sqld при старте), `infra/nats`
(соединение, которое не сдаётся), `infra/temporal` (клиент с трейсингом и
метриками), `infra/valkey` (valkey-go с OTel). Узлы `nats` и `temporal`
самого SDK подключаются через `infra/nats.Connect` и
`infra/temporal.Options`: код подключения один. Шина событий платформы —
только NATS из блока SDK (`BACKPLANE_NATS_*`); `infra/nats` — для NATS,
которым сервис владеет сам и которого backplane не видит. `Provide` получает
`deps.Scope` своего узла; у обязательной зависимости он может создавать под
ним дочерние узлы (они стартуют вместе с ней). Для
разового случая — `deps.Func(fn, deps.WithProbe(...), deps.WithClose(...))`,
имя узла — от типа (`*pgxpool.Pool` → `pool`). Фабрика синглтона
(`deps.Factory[T]`) — провайдер без пробы.

Каждый узел — `deps.Scope`: `Name`, свой путь `Path` (`greeter/templates`),
логгер `Log` с `node=<путь>`, `Tracer` и `Meter` (`<service>/<путь>`),
`Span(ctx, name, fn)` (и `deps.SpanValue` для fn со значением), `Go`,
`OnStart`/`OnStop`. Одинаковые имена получают суффикс (`store-2`). Узлы
создаются до `Run` (после — паника; исключение — `Provide` обязательной
зависимости); дерево попадает в манифест (`nodes`).

**Lifecycle.** Всё — и части SDK, и дерево автора — одно дерево узлов.
Старт — в глубину в порядке создания; остановка — в точно обратном
порядке, поэтому узел всегда останавливается раньше всего, что создано до
него: компонент — раньше зависимости, которую ему передали, ребёнок —
раньше родителя. Узлы сервиса:

`config → telemetry → health → platform → consul → nats → temporal → hooks → <дерево автора> → internal-api → reactors → worker → public:<addr> → drain → register → serving`

Не все узлы есть всегда: `nats` — когда задан `nats.url`; `temporal` и
`hooks` — когда задан `temporal.addr` (`hooks` — worker вызовов хуков вне
workflow; ничего не запускает, если хуков нет); `reactors` — когда
объявлены реакторы и есть NATS; `worker` — когда объявлены активити или
workflows, есть Temporal и `temporal.worker.enabled`; `register` — когда
задан Consul; `public:<addr>` — по одному на публичный адрес. Без
транспорта объявленное остаётся в манифесте, `Run` пишет предупреждение.
Соединения стоят до дерева автора, поэтому компонент может публиковать
события и вызывать хуки уже из `OnStart` и ещё из `OnStop`; `reactors`,
`worker` (активити и workflows автора) идут после дерева и
останавливаются раньше него.

`consul` публикует манифест и состояние инстанса (фаза `starting`) до
дерева автора и снимает состояние последним; `register` — регистрация в
каталоге и фаза `serving`, на остановке — уход из каталога и фаза
`stopping` (§4.2).

Остановка узла: отмена его контекста, stop-хуки в обратном порядке,
ожидание его горутин. Узел, чей старт упал, тоже останавливается — stop-
хуки терпят частичный старт. Горутины — `scope.Go(fn)`: запускаются после
старта узла, на стопе отменяются и дожидаются; ошибка или паника любой
останавливает сервис.

Остановка — в пределах одного бюджета `Shutdown.Timeout`: `serving`
снимает readiness (`NOT_SERVING`) → `register` снимает регистрацию в
каталоге и пишет фазу `stopping` → `drain` ждёт `Shutdown.Drain`, пока
балансировщики заметят уход → публичные listener'ы перестают принимать и
дорабатывают запросы в полёте → `worker`, `reactors` →
`internal-api` закрывает приём и ждёт внутренних вызовов в полёте →
дерево автора (компоненты раньше своих зависимостей) → `hooks`,
`temporal`, `nats` → `consul` удаляет состояние инстанса (сессию) →
платформенный порт → health → telemetry → config. Этапы делят бюджет:
публичные listener'ы и `internal-api` вместе получают одно окно
`Shutdown.Listeners` — по его концу открытые стримы обрываются, а
контексты внутренних вызовов в полёте отменяются; окно и `drain`
укорачиваются так, чтобы дереву автора, зависимостям и телеметрии
осталось не меньше `Shutdown.Reserve`. У `worker` и `reactors` своего
окна нет: они ждут активити и обработчиков в полёте до конца общего
бюджета, поэтому обработчик, который не заканчивается,
съедает и `Reserve` (§8, «Остановка реакторов»). Узел, не уложившийся в
бюджет, — ошибка остановки: `Run` возвращает её, остальные узлы всё
равно останавливаются. Повторный SIGINT/SIGTERM во время
остановки (или сигнал, пришедший, когда остановка уже идёт по другой
причине) — немедленный выход процесса с кодом 1 после записи в лог. Сигнал во время старта —
чистая остановка уже поднятого, `Run` возвращает `nil`; обязательная
зависимость, которая ещё ретраится, при этом бросается, а уже
предоставленные закрываются. Health один: пробы регистрируются по виду;
`grpc.health.v1`, HTTP-пробы и Consul-check читают одно и то же.

Серверы инструментированы: gRPC обоих классов портов — otelgrpc (без
вызовов `grpc.health.v1`), HTTP публичных портов — otelhttp, ws-proto —
span на вызов; HTTP платформенного порта (пробы, бандл, info, pprof)
span'ов не пишет. Логи несут trace_id; паника handler'а на любом сервере
— ошибка запроса, не процесса (§3.1). Лимиты серверов — `config.Server`
(§4.3) на обоих классах портов.

**Consul presence.** Сессия с `LockDelay` 1 мс (дефолтные 15 с не дали бы
перезапущенному инстансу опубликовать состояние); ключ, удерживаемый
прошлой инкарнацией того же инстанса, перехватывается при первом
установлении присутствия процесса (§4.2). Consul ни на старте, ни потом
не роняет сервис: присутствие восстанавливается в фоне (§4.5).

**Логирование.** Логгер по умолчанию — JSON в stdout плюс OTLP-tee; его
уровень — Live-поле `log_level`, изменение применяется на лету, неверное
значение оставляет прежний уровень (предупреждение в лог). Логгер,
переданный `backplane.Logger(l)`, принадлежит автору: `log_level` к нему
не применяется, OTLP-tee SDK не добавляет. Конфигурация пишет в лог сервиса
каждый отказ в применении обновления (warn, с ошибкой и ревизией) и каждый
переход Consul-слоя: недоступен (warn) и восстановлен (info); метрики
`backplane.config.updates{outcome=applied|rejected}` и
`backplane.config.degraded`.

### 4.5 Старт сервиса

`Open`: загрузить конфигурацию (файл, env; Consul KV для Live-полей, если
Consul задан и доступен — иначе предупреждение и повтор в фоне) →
проверить блок SDK → собрать State конструктором автора. `Run`: собрать и
проверить манифест (ошибки объявлений — ошибка `Run` до того, как что-то
слушает) → телеметрия → health `NOT_SERVING` → платформенный порт →
манифест и состояние в Consul (фаза `starting`, если Consul есть; в
фоне) → соединения NATS и Temporal (одна короткая попытка, дальше в фоне)
и worker хуков → дерево узлов автора (обязательные зависимости ждут
готовности) → внутреннее API открывается → реакторы и worker (в фоне, как только есть соединение) → порты внешнего API →
регистрация в каталоге Consul, фаза `serving` → health `SERVING`. Без Consul, NATS и Temporal сервис работает в
объёме внешнего API и конфигурации из env/файла.

**Деградация.** Ни одна из внешних систем не роняет сервис — ни на старте,
ни потом; readiness от них не зависит, кроме NATS и Temporal с
`backplane.RequireNATS()` / `RequireTemporal()`:

| система · что | недоступна на старте | пропала во время работы | вернулась |
|---|---|---|---|
| Consul · конфигурация | Consul-слой пуст, Live-поля из env/файла/defaults, повтор с backoff 1s..30s (`config.ConsulBackoff`); исключение — обязательное Live-поле, которое есть только в KV: держит `Open` до ответа Consul или конца ctx (§5.3) | последние значения KV остаются в силе; переход пишется в лог (warn) и в `backplane.config.degraded` | слой перечитывается, изменения применяются по обычным правилам (§5.5); переход — в лог (info) |
| Consul · присутствие | манифест, состояние и регистрация ретраятся в фоне (backoff 2s..1m); инстанс работает, но не виден | продление сессии ретраится TTL/2, затем присутствие пересоздаётся с backoff; состояние исчезает, когда истекает сессия; регистрация в каталоге живёт в агенте | новая сессия, состояние переписывается, регистрация подтверждается (если инстанс уже `serving`) |
| NATS · `Publish` | клиент переподключается бесконечно (2 s + jitter до 1 s); `Publish` сразу — `event.ErrUnavailable`; стрим `bp_<service>` создаётся в фоне после подключения (backoff 0.5s..15s) | сразу `ErrUnavailable` — без буферизации | снова публикует |
| NATS · реакторы | стримы и consumers создаются в фоне после подключения (backoff 0.5s..15s) | потребление стоит; неподтверждённое будет доставлено заново; consumer, удалённый на сервере, пересоздаётся с позиции реактора (старейшее неподтверждённое, иначе после последнего увиденного) | потребление продолжается с позиции durable consumer'а |
| Temporal · `Call` (хуки) | одна попытка подключения (`dial_timeout`), дальше в фоне (backoff 1s..30s); `Call` сразу — `hook.ErrUnavailable` | подключённый клиент проверяет соединение каждые 5 s; вызов падает ошибкой транспорта в пределах своего дедлайна | вызовы проходят |
| Temporal · worker'ы | worker'ы стартуют, как только соединение поднимется | worker'ы SDK Temporal переподключаются сами; активити в полёте добегают или истекают по таймаутам Temporal | опрос очередей продолжается |
| OTLP · экспорт | сигнал без endpoint не экспортируется; с endpoint экспортёр ретраит и отбрасывает | батчи отбрасываются после ретраев экспортёра | экспорт продолжается |
| readiness | Consul и OTLP не входят никогда; NATS и Temporal — только с `RequireNATS()` / `RequireTemporal()`: не ready, пока соединения нет | то же | ready снова, как только соединение есть |

Состояние каждого соединения — в `transports` состояния инстанса (§4.2) и
в метрике `backplane.transport.connected{transport}`.

### 4.6 Тесты компонентов — `backplanetest`

Компоненты тестируются без `Open`, `Run`, портов и Consul: транспорты
заменены записывающей заглушкой, дерево — то же, что в сервисе.

```go
h := backplanetest.New(t, backplanetest.Name("hello"))           // дерево сервиса ("test" по умолчанию)
cfg := backplanetest.Config[greeter.Config](t, map[string]string{"SUFFIX": "?"})
g, _ := greeter.New(h.Root(), &cfg, deps.Static(db))              // узлы — под h.Root()
f := flows.New(h.Root(), g, deps.Static(db))
backplanetest.Answer(h, f.Greet, fn)                             // ответ на хук: Call и CallHook
h.Start()                                                        // provide, горутины; стоп — в t.Cleanup
backplanetest.Ready(h)                                           // readiness дерева, как у пробы
backplanetest.Events(h, g.Greeted())                             // что опубликовано в event.Ref
backplanetest.Activity[flows.EchoIn, flows.EchoOut](ctx, h, "Echo", in) // вызов активити
backplanetest.ReactConsumer(ctx, h, audit.Consumer, v)           // доставка реактору
backplanetest.SetLive(&cfg.Suffix, "!")                          // Live как из консоли; Watch срабатывает
w := workflowtest.New(t, h)                                  // testsuite Temporal с worker'ом сервиса
h.Manifest()                                                     // что объявлено
```

- **Harness.** `New(t, opts...)`: `Name(service)` — имя сервиса (события
  публикуются как `<name>.<Event>`, реактор на своё событие —
  `<name>.<Event>`), `StopBudget(d)` — бюджет остановки в `t.Cleanup`
  (10 s; не уложился — тест падает). `h.Root()`, `h.Start()`,
  `h.Service()`, `h.Manifest()` (ошибка объявлений — тест падает).
  `Ready(h)` — `nil` или ошибка `ErrNotReady` с каждой неготовой
  обязательной зависимостью и причиной.
- **Конфигурация.** `Config[C](t, vars)` / `LoadConfig[C](ctx, vars)` —
  секция или вся конфигурация, как её загрузит сервис: умолчания схемы и
  `vars` (имена как в env сервиса без префикса: `{"SUFFIX": "?"}` для
  секции, `{"GREETER_SUFFIX": "?"}` для всей); незнакомое имя — ошибка;
  все `Validate` вызываются. Файл, env процесса и Consul не читаются.
  `SetLive(&live, v)` — обновление Live-поля, как из консоли.
- **Хуки.** `Answer(h, ref, fn)` отвечает на `Call` и, в `Workflows`, на
  `CallHook`; `CallKey(ctx)` в `fn` — `hook.Key` вызова. Хук без
  `Answer` отвечает `hook.ErrUnavailable` (`CallHook` —
  `hook.ErrNoBinding`), как в продакшене без транспорта или биндинга.
- **События.** `Events(h, ref)` — значения, `EventsWithMeta(h, ref)` — с
  `Key`, `ID`, `Time`, `Headers`. `FailPublish(h, err)` — `Publish`
  падает с `err`; `Unavailable(h)` / `Available(h)` — оба транспорта
  пропадают (`event.ErrUnavailable`, `hook.ErrUnavailable`) и
  возвращаются.
- **Реакторы.** `React(ctx, h, path, event, v, opts...)` — реактору узла
  `path` (`""` — корень; реактор с `event.Consumer` находится по событию,
  если он такой один), `ReactConsumer(ctx, h, consumer, v, opts...)` — по
  имени consumer'а. Обработчик вызывается один раз с `Timeout` реактора и
  полным `event.Delivery` (новый `ID`, `Attempt` 1, `Consumer`,
  расширения `instance` и `version`); `WithKey`, `WithID`, `WithAttempt`,
  `WithHeader` меняют доставку. Результат — ошибка обработчика; повторов
  и DLQ нет.
- **Активити.** `Activity[Req, Res](ctx, h, name, in)` вызывает активити
  один раз, как шаг биндинга: `activity.InfoOf` даёт `Attempt` 1,
  уникальный `Key` и дедлайн `ctx`; `Heartbeat` ничего не делает.
- **Workflows.** `workflowtest.New(t, h)` — `testsuite.TestWorkflowEnvironment` с
  регистрациями worker'а сервиса: активити `Handle` по имени через
  envelope, workflows `workflows.Declare`, `workflows.Activity`,
  `workflows.Register`; хуки — Nexus-сервис `<service>.Hooks` среды,
  отвечающий через `Answer`. Одна среда — один workflow.
  `workflowtest.WorkflowActivity[Req, Res](w, name, in)` запускает активити на
  workflow, как шаг биндинга.

Активити или реактор, которых нет, — `backplanetest.ErrNotDeclared`.

## 5. Конфигурация

### 5.1 Слои

```
defaults < файл < env < Consul KV config/<service>/<путь Live-поля>
└─────── деплой ────────┘  └── backplane (только Live) ──┘
```

KV — верхний, необязательный слой и видит только пути Live-полей: ключ
обычного поля в KV игнорируется. Появился — применился горячо, пропал —
остались значения из env/файла.

### 5.2 Роль backplane

Consul KV — **канал доставки**, не источник правды: у KV нет истории,
аудита и отката, а правки через Consul UI реплицируются мгновенно без
следа (норма зрелых установок — в KV пишет автоматика, не человек).
Сервер — пакет `internal/config` (компонент `config` дерева backplane).

- Источник правды override-слоя — **PostgreSQL backplane**: таблица
  `config_revision` — ревизии сервиса (номер с 1 на сервис, значения,
  автор, комментарий, время, `rollback_of`), append-only; `config_current`
  — текущая ревизия сервиса (всегда последняя). Откат — новая ревизия со
  значениями старой и `rollback_of` = её номер.
- **Override** — плоская карта «Live-путь манифеста (`config.live`,
  через точку: `greeter.suffix`) → JSON-значение». Переопределяются
  только Live-пути целиком: путь не из `config.live` (обычное поле,
  секция, путь внутри Live-поля) — отказ `NOT_LIVE`; `null` — отказ
  (чтобы снять override, путь не передают). Путь, которого нет в
  ревизии, берёт значение из слоёв инстанса (defaults, файл, env).
- Валидация **до** сохранения: override кодируется в KV (§5.3) и
  раскладывается тем же декодером, что читает SDK (xconf env-кодирование
  против схемы инстанса), затем накладывается как слой xconf (объекты
  сливаются по ключам, остальное заменяется) на эффективную конфигурацию
  **каждого живого инстанса** (`config` его состояния, §4.2) и
  проверяется схемой манифеста **версии этого инстанса** (schemapb:
  типы, границы, CEL); пути, которые в его версии не Live, для него
  пропускаются — его SDK их игнорирует. Нет живых инстансов — база —
  defaults схемы старшего манифеста. Ошибки, которые конфигурация инстанса
  даёт и без override, не сообщаются: так маскированные секреты (`***` в
  состоянии) считаются присутствующими и валидными, а схема
  проверяет только то, что ломает override. Пути, которые текущая ревизия
  переопределяла, а новая — нет, валидируются на значении из состояния
  инстанса (значения нижних слоёв backplane не видит). Без схемы —
  проверяется только JSON, Live-путь и что его первый сегмент есть в
  `config.keys`. Невалидное не сохраняется; ответ — нарушения с путём,
  инстансом (пусто — не зависит от инстанса или проверено на defaults),
  кодом (`ErrorCode` schemapb без `ERROR_CODE_`, либо `NOT_LIVE`,
  `INVALID_JSON`, `NULL_VALUE`, `UNDECLARED`) и сообщением. `Validate`
  кода автора здесь не вызывается (§5.5).
- Консоль показывает поле по каждому инстансу: эффективное значение и его
  источник (default / файл / env / KV), применённую ревизию и ошибку
  применения — из состояния инстанса.
- API — `backplane.console.v1.ConfigService` (`backplanepb/console/v1/config.proto`),
  значения — карта «Live-путь → JSON-текст»: `GetConfig` (схема, ключи и
  Live-пути старшего манифеста, текущая ревизия, по инстансу — фаза,
  эффективная конфигурация, значение и источник каждого Live-пути,
  применённая и отвергнутая ревизия, ошибка), `ListRevisions` (новые
  первыми; `before`, `page_size` — 50 по умолчанию, до 500;
  `next_before`), `ValidateOverride` (сухой прогон), `SaveRevision`,
  `Rollback` (нарушения вместо ревизии, если отвергнуто;
  `delivery_error` — ревизия сохранена, но в KV ещё не доставлена),
  `WatchConfig` (стрим: текущее состояние, затем при каждом изменении —
  снапшот реестра, ревизия этой или другой реплики). Автор ревизии —
  `console:<сессия>` из metadata `bp-console-session`, без неё — `admin`.
  Сервис зарегистрирован во внутреннем API backplane (платформенный порт);
  консоль отдаёт его по `/ws`.
- Сервис читает KV любым способом (in-process watch через SDK,
  consul-template, envconsul) и о backplane не знает.

Метрики: `backplane.config.revisions{outcome=saved|rejected}`,
`backplane.config.kv.rewrites{reason=missing|stale|edited}`,
`backplane.config.kv.failures`. Логи: сохранение (сервис, ревизия, автор,
откат), отказ (число нарушений, первое), доставка, перезапись ручной
правки (warn с ключами), начало и конец серии ошибок reconciler'а.

### 5.3 Репликация PostgreSQL → Consul

Сторона SDK — чтение `_revision` и отчёт в состоянии инстанса; остальное
— сервер. Consul может умереть или потерять данные — мы не теряем ничего:

- **Кодирование KV** — ровно то, что читает Consul-источник SDK (xconf
  `NewPrefix`, env-кодирование): ключ — `config/<service>/<Live-путь, `.`
  → `/`>`; значение Live-поля контейнерного вида схемы (object, ref, map,
  list, one-of, json) — его JSON, скалярного — текст: строка как есть,
  bool — `true`/`false`, число — десятичное (целое поле — без дроби и
  экспоненты: `1e3` → `1000`), duration — текст Go (`1m30s`; число —
  наносекунды). Без схемы решает форма значения: объект и список — JSON,
  остальное — текст. Ревизия хранит и сохранённые значения, и
  закодированные строки, так что доставка от реестра не зависит.
- **Сохранение** = serializable-транзакция в PostgreSQL (следующий номер,
  строка ревизии, `config_current`; параллельные сохранения сервиса
  повторяются), затем доставка в `config/<service>/` (до 10 s; ошибка
  доставки ревизию не откатывает — её допишет reconciler).
- **Доставка** читает префикс, затем текущую ревизию из PostgreSQL и
  пишет разницу: изменённые значения, удаление ключей, которых нет в
  ревизии, и последним — `config/<service>/_revision = <n>`. Последняя
  транзакция начинается с CAS на `_revision` (`check-index` прочитанного,
  `check-not-exists`, если его не было): реплика со старой ревизией не
  перетирает новую, проигравший CAS перечитывает и повторяет (до 3 раз).
  До 62 изменений — одна атомарная `txn`; больше — транзакции по 64
  (лимит Consul): сначала значения, затем удаления, `_revision` — в
  последней; пока она не прошла, читатель может увидеть часть новых
  значений под старой `_revision`. Папки Consul UI (ключ на `/` без
  значения) — не значения: не сравниваются и не удаляются.
- SDK читает `_revision` тем же запросом к префиксу, что и значения (сам
  ключ в конфигурацию не попадает), и публикует в состоянии инстанса
  (`config_revision`) ровно ревизию применённых значений; отвергнутая — в
  `config_rejected_revision` (§5.5).
- **Reconciler** — горутина компонента `config`: проход при старте, раз в
  `BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL` (30s) и по каждому изменению
  префикса `config/` (blocking query, ожидание 5m). Проход — один `List`
  `config/` и один запрос текущих ревизий всех сервисов; сервис, у которого
  `_revision`, значения или набор ключей отличаются от ревизии, проходит
  доставку. Причина в логе и метрике: `missing` (пусто — Consul потерял
  данные), `stale` (другая `_revision`), `edited` (ревизия совпадает,
  значения нет — ручная правка в Consul UI перетирается, warn с ключами).
  Сервисы без ревизий в PostgreSQL backplane не трогает: `config/<service>/`
  становится управляемым с первой сохранённой ревизии. Реплики работают
  одновременно: все пишут то, что в PostgreSQL, через CAS, так что
  проходы сходятся; сохранение на одной реплике другие видят по изменению
  `config/` (и шлют в свои `WatchConfig`).
- Пока Consul недоступен, сервисы работают на последних значениях или на
  env/файле; backplane копит ревизии и доставляет, когда Consul вернётся.
  Исключение — обязательное Live-поле без default, которое есть только в
  KV: оно держит `Open`, пока Consul не ответит или не кончится ctx
  `Open` — тогда `Open` возвращает ошибку. ctx `Open` ограничивает только
  загрузку: запущенный сервис от него не зависит. Ошибка в обычном поле
  при этом не ждёт Consul — `Open` падает сразу.
- Необязательное Live-поле без default, которого нет ни в одном слое,
  читается нулевым значением и получает значение из KV, как только оно
  появится; все копии секции видят его (ячейки Live-полей создаются при
  `Open`).

### 5.4 GitOps [backplane]

Обычные поля остаются в git → Argo → env; их изменение — rollout, и это
правильно. Live-значения живут вне git — это и есть смысл динамической
конфигурации (AWS AppConfig, OpenFeature). Argo не управляет объектами, в
которые пишет backplane, поэтому self-heal их не откатывает. «UI → коммит
в git → Argo → ConfigMap → SDK следит» возможен позже как второй канал.

### 5.5 Проверка значений и отказ в применении

Схема (schemapb: типы, границы, CEL) — первая проверка. Вторая — код
автора: если конфигурация или любая её секция (поле-структура, указатель,
элемент списка или map) реализует `config.Validator`
(`Validate() error`, приёмник значение или указатель), SDK вызывает её —
сначала секции в глубину, затем объемлющую структуру; ошибка секции
предваряется её путём (`limits: max must be positive`). `Validate`
встроенной структуры, продвинутый в объемлющую, вызывается один раз.

- **`Open`**: ошибка схемы или `Validate` — ошибка `Open`
  (`config: validate: …`); сервис не стартует — в том числе если
  невалидное значение пришло из KV.
- **Обновление из KV**: значения сначала раскладываются в новую копию
  конфигурации и проверяются схемой и `Validate`. Прошли — Live-поля
  обновляются на месте, `Watch` срабатывают, `config_revision` = ревизия
  этих значений, `config_error` пуст, `config_rejected_revision` = 0. Не
  прошли — обновление **не применяется целиком**: Live-поля и
  эффективная конфигурация в состоянии инстанса остаются прежними,
  `config_revision` — прежним, `config_error` — причина,
  `config_rejected_revision` — ревизия отвергнутого обновления; в лог —
  warn с ошибкой и ревизией, метрика
  `backplane.config.updates{outcome=rejected}` (повтор того же отказа той же
  ревизии не дублируется). Следующее прошедшее обновление снимает отказ.

backplane проверяет override до сохранения только схемой (§5.2): `Validate`
живёт в коде сервиса, и ревизию, которую схема пропустила, а `Validate`
отверг, консоль видит по `config_rejected_revision` и `config_error` каждого
инстанса.

## 6. Внешняя маршрутизация [деплой]

Маршрутизация входящего внешнего трафика полностью принадлежит деплою.
Backplane и SDK не зависят от конкретного прокси, не предоставляют xDS/EDS,
не собирают конфигурацию шлюза и не публикуют маршруты из манифестов.
Автор выбирает обслуживаемые API и код; администратор — domains, paths,
rewrites, auth, фильтры и обнаружение upstream. TLS завершается до приложения.

В Kubernetes внешняя инфраструктура обращается к Kubernetes Service; в Compose
можно использовать container DNS. Consul может независимо использоваться деплоем
для service discovery. Backplane читает каталог и health для собственных задач:
показ состояния, внутренний API relay и доставка UI/schema bundles.

Панель и её WebSocket API работают на собственном console listener (`8081`).
Модули и их внутренние API доступны через этот listener без отдельного шлюза.
Обратный прокси установки сохраняет console prefix и поддерживает WebSocket upgrade.
Нет общего API управления маршрутами, редактора gateway YAML или экспортера конфигурации.

## 7. Хуки, активити, биндинги

### 7.1 Биндинг [backplane]

Биндинг — **реализация хука («нужно»), собранная из активити («умею»)**.
Живёт в backplane, редактируется в консоли (раздел Wiring: YAML или
граф), к коду сервисов не относится:

```yaml
hook: iam.SendEmail
steps:
  render:
    activity: template.Exec
    input: {name: req.template, data: req.data}
  send:
    activity: smtp.Send
    input: {to: req.to, subject: render.subject, text: render.text}
result: {message_id: send.id}
```

IAM знает только свою `SendEmail`; template и smtp — только свои `Exec` и
`Send`; биндинг знает всех троих. В другой установке `iam.SendEmail`
реализуется через `courier.Send` одним шагом — IAM не меняется. Шаг
бывает условным (`when`) и повторяемым по списку (`forEach`); нужно
больше — ветвления, состояние между запусками, — это сервис.

Нет биндинга: `required` хук → читаемая ошибка `no binding for
iam.SendEmail` и красный слот в карточке, как только манифест появился;
иначе — ответ по умолчанию (`{}`), сервис продолжает.

**Модель** (proto `backplane.console.v1.BindingDefinition`, общие части —
`step.proto`; Go `internal/bindings.Binding`). Каноничная форма —
protojson определения (ключи camelCase); YAML — её запись, консоль
редактирует именно её:

- `hook` — полное имя хука `<service>.<Hook>`; `steps` — шаги по имени
  (map: имя — ключ); `result` — выход хука (нет — `{}`); `description` —
  текст для людей; `editor` — раскладка графа (позиции узлов по имени шага,
  `$trigger`, `$result`, заметки), компиляция и исполнение её не читают.
- Шаг: имя (ключ) — идентификатор `[A-Za-z_][A-Za-z0-9_]*`; не `req`,
  `event`, `meta`, `steps`, не слово CEL (`in`, `true`, `null`, `int`,
  `map`, …), не `on`/`when`. `activity` — полное имя активити. `input` —
  вход активити (нет — `{}`). `when` — CEL bool: false — шаг пропущен.
  `after` — явные зависимости. `undo` — активити-компенсация, `undoInput` —
  её вход (нет — выход шага). `retry {attempts, initialInterval,
  maxInterval, backoff}`, `startToClose`, `heartbeat` (консоль принимает и
  длительности Go: `500ms`, `1m30s`), `description`; опции цикла — ниже.
- Значение (`input`, `undoInput`, `result`) — дерево JSON
  (`google.protobuf.Value`): строка — выражение CEL, объект — объект по
  полям (рекурсивно), список — список по элементам, число, bool, null —
  литералы. Строковый литерал пишется как CEL: `"'hello'"`.

**Цикл (`forEach`).** Шаг с `forEach` выполняется по разу на элемент:

```yaml
steps:
  notify:                         # тело — одна активити
    forEach: req.recipients       # CEL: список, или map — элементы {key, value} по ключам
    as: who                       # имя элемента (нет — item); позиция — whoIndex
    when: who.email != ""         # по элементу: false — элемент пропущен
    concurrency: 5                # элементов одновременно (нет — 10, максимум 100)
    onError: continue             # fail (нет) | continue
    maxItems: 500                 # больше — шаг падает (нет — 1000, максимум 10000)
    activity: smtp.Send
    input: {to: who.email, subject: render.subject}
  offboard:                       # тело — под-поток шагов на элемент
    forEach: req.accounts
    steps:
      charge: {activity: billing.Charge, input: {account: item.id}, undo: billing.Refund}
      close:  {activity: crm.Close, input: {id: item.id, receipt: charge.id}}
    result: {id: item.id, receipt: charge.id}   # элемент выхода; нет — null
```

- Тело — либо активити (`activity`, `input`, `undo`, `retry`, … как у
  обычного шага; всё, кроме списка, видит элемент), либо `steps` —
  под-поток со своим `result` (без `activity`, `input`, `undo`). Шаги тела
  видят элемент, друг друга и шаги всех объемлющих уровней; имена
  уникальны внутри тела и не перекрывают имена объемлющих шагов и
  элементов. Тела вкладываются.
- Выход шага для остальных — **список в порядке элементов**: выходы
  активити (тип — `list(<выход активити>)`) или `result` тела
  (`list(dyn)`); пропущенный `when` или упавший при `continue` элемент —
  `null`. `steps.<шаг>` цикла — `{skipped, failed, errors: [{index,
  message}]}`.
- `onError: fail` — первый упавший элемент валит шаг: элементы в полёте
  доделываются, новые не стартуют, запуск компенсируется. `continue` —
  упавший элемент в `failed`/`errors`, шаг завершается.
- Зависимости цикла — чтения его списка, `when`, входа и всех выражений
  тела шагов своего уровня; тело внутри упорядочено так же, как верхний
  уровень. Упавший элемент-под-поток сразу компенсирует сделанное им
  (элемент — всё или ничего); удавшийся отдаёт свои `undo` запуску — при
  последующей ошибке они откатываются вместе с остальными.
- Вызов в Temporal помечен меткой элемента: `notify[3]`,
  `offboard[0].charge` (сегменты через `.` от внешнего цикла внутрь) —
  id дочернего workflow и таймлайн (`StepRun.item`, `StepRun.parent` —
  id тела `<внешний>/<внутренний>`) по ней.

**Выражения.** CEL над JSON. Переменные: `req` — вход хука; `<шаг>` —
выход выполненного шага (у пропущенного — `{}`, у цикла — список);
`steps.<шаг>.skipped` — пропущен ли шаг своим `when`; в цикле — элемент
(`item` или `as`) и его позиция `<элемент>Index`. Язык — стандартный CEL, расширения strings,
encoders, math, lists, optional-значения (`req.?x.orValue("")`), числа
сравниваются через int/uint/double. Вычисление **детерминировано**: ни
часов, ни случайности, ни I/O; макросы `all`, `exists`, `exists_one`,
`map`, `filter` обходят map по ключам в порядке сортировки; результат —
JSON с отсортированными ключами. Поэтому оно идёт прямо в коде workflow.

- Числа входов и выходов — по схеме: поля int/uint — int/uint (int64
  protojson'а в кавычках тоже), float/double — double, bytes — из base64;
  без схемы число с целой записью — int, иначе double. Duration и
  timestamp схем — `dyn`: их JSON зависит от кодека автора (Go пишет
  наносекунды, proto — `"1s"`); CEL-timestamp на выходе — строка RFC 3339,
  duration — `"1.5s"`.
- Ошибка на значениях запуска читаема: `transform failed at step render
  (input.to): no such key: to`, `transform failed at result: …`; место
  вложенного значения — `input.to.items[2]`.

**Порядок.** Зависимости шага — его `after` и каждый шаг, который читают
его `input` и `when` (по имени или `steps.<имя>`); порядок шагов в
определении не значит ничего, цикл — ошибка сохранения. Уровни графа —
для анализа и раскладки; исполнитель запускает шаг, **как только
завершены его зависимости** (готовые — по имени), не дожидаясь уровня.
Пропущенный шаг не останавливает зависящие от него. `undo` — сага: при
ошибке выполненные шаги компенсируются в обратном порядке завершения;
`undoInput` читает только сам шаг и его предков.

**Опции шага**: из биндинга, иначе умолчания активити из манифеста
(`start_to_close`, `heartbeat`, `retry.attempts`), иначе платформа —
`start_to_close` 30 s, 3 попытки, интервал 1 s, максимум 30 s, множитель
2, heartbeat нет.

**Валидация** (`ValidateBinding`, `AnalyzeBinding` и каждое сохранение) —
по последним манифестам (`registry.Service.Latest`: высшая версия живого
инстанса): хук и активити (`undo` тоже) объявлены; имена шагов; `after`
и ссылки — на существующие шаги, без циклов; `undoInput` — только на шаг и
его предков; опции (попытки ≥ 0, интервалы ≥ 0, `initial ≤ max`,
множитель ≥ 1; опции цикла — только при `forEach`, тело — активити или
`steps`, не оба; `forEach` — список или map); каждое выражение компилируется и **проверяется по типам
схем**: schemapb-схема → тип CEL (объект — struct-тип с полями схемы,
list, map, скаляры; остальное — `dyn`), так что `req.nope` и
`render.nope` — ошибка сохранения. Узлы значения сверяются со схемой
входа активити (`result` — с выходом хука) на любой глубине: незнакомое
поле, пропущенное обязательное, тип выражения или литерала. Совместимость
— как у JSON: int подходит к double, но не наоборот, map — к объекту,
timestamp/duration — к строке, `dyn` — ко всему; `when` — bool. Без
схемы проверки типов нет — ошибка придёт в рантайме читаемой. Стоимость
выражения оценивается (коллекции неизвестного размера — до 1000
элементов): выше лимита вычисления (10⁶ единиц CEL) — ошибка `COST`.

Нарушение — `{path, code, message, expr}`: `path` — JSON Pointer места по
ключам (`/hook`, `/steps/send/input/to`, `/steps/x/after/0`,
`/steps/x/retry/backoff`, `/result/message_id`; шаг тела —
`/steps/offboard/steps/charge/input/account`); `expr` — диапазон внутри
выражения (кодовые точки, конец не включён), нет — если проблема в самом
месте; `code` — `INVALID_NAME`, `RESERVED_NAME`, `UNKNOWN_HOOK`,
`UNKNOWN_EVENT`, `UNKNOWN_ACTIVITY`, `UNKNOWN_STEP`, `CYCLE`,
`UNDO_REFERENCE`, `CEL_ERROR`, `COST`, `TYPE_MISMATCH`, `MISSING_FIELD`,
`UNKNOWN_FIELD`, `INVALID_OPTION`. Консоль сопоставляет путь с узлом YAML
и добавляет диапазон — подсветка ровно под ошибкой.

**Версии** — как ревизии конфигурации (§5.2), в PostgreSQL:
`binding_version` (hook, version с 1, `definition` — protojson
`BindingDefinition`, NULL — надгробие удаления, author, comment,
created_at, rollback_of) и `binding_current` (hook → действующая версия).
Сохранение — валидация, следующая версия и `current` в одной
serializable-транзакции; отклонённое не пишется. `base_version` в
Save/Rollback/Delete — версия, с которой началась правка: другая текущая
— отказ `ABORTED` (два редактора не перетирают друг друга молча); не
задана — без проверки. Откат — новая версия с определением старой
(валидируется заново: манифесты могли измениться; откат к надгробию
удаляет). Удаление — надгробие, история остаётся. Автор — сессия консоли
`console:<id>`, вне сессии — `admin`. Состояние хука: `BOUND` (действует
определение), `UNBOUND`, `REQUIRED_UNBOUND`, `BROKEN` — текущее
определение больше не компилируется по последним манифестам (сервис
изменил контракт), `HookBinding.violations` говорят, где; вызовы падают,
пока не исправят. Биндинг хука, который манифест больше не объявляет,
остаётся в списке с `declared: false`.

**Консоль** — `BindingService` (`/ws`): `ListBindings` (хуки всех или
одного сервиса с состоянием, текущей версией и нарушениями), `GetBinding`
(версия, 0 — текущая, и её нарушения сейчас), `ListBindingVersions`
(страницы, новые первыми), `ValidateBinding`, `SaveBinding`,
`RollbackBinding`, `DeleteBinding`, `WatchBindings` (список сейчас и после
каждого сохранения на любой реплике — опрос PostgreSQL раз в 2 s — и
каждого манифеста). Запуски — RPC исполнителя рядом: `TestBinding`,
`ListBindingRuns`, `GetBindingRun` (с таймлайном шагов),
`CancelBindingRun`. Редактору — `WiringService`: `GetWiringCatalog` (хуки,
активити и события всех сервисов со схемами — палитра и типы портов),
`AnalyzeBinding`/`AnalyzeRule` (нарушения; уровни и зависимости шагов по
видам — данные, `after`, `when`, `undo`; тип CEL каждого узла значения;
каждое чтение переменной с местом — рёбра графа; у шага тела — `parent`,
id его цикла, у цикла — `item_type`, тип элемента), `RenameStep` (шаг по
id — `<цикл>/<имя>` для шага тела; по синтаксическим деревьям выражений
там, где шаг виден: строка или поле с тем же именем не меняются; позиции
графа тела переезжают вместе с циклом).

**Для исполнителя** (`internal/bindings`): `Manager.Active(ctx, hook)` —
действующее определение и версия (`ErrNoBinding`); `Manager.Changes(ctx)`
— сигнал после записей (свои сразу, чужих реплик — в пределах опроса);
`Manager.Catalog()` — последние манифесты; `CompileBinding(b, catalog)` —
`*Program`: шаги по имени (`StepPlan`: активити, сервис = очередь, kind —
activity или child workflow, зависимости, уровень, `undo` с сервисом и
kind, разрешённые опции), выражения скомпилированы. `Program`
сериализуется в JSON (вход workflow; значения — их JSON) и
восстанавливается `json.Unmarshal` в тот же. Вычисление — чистые функции
над `Scope` (значение; `Bind`/`Skip` возвращают новый): `Start(input)`,
`When(step, s)`, `Input(step, s)`, `Bind(s, step, output)`,
`Skip(s, step)`, `UndoInput(step, s)`, `Result(s)`, `Eval(expr, vars)`;
цикл — `Items(step, s)` (элементы), `ItemScope(step, s, i, item)`
(область элемента), `Body(step)` (под-программа тела), `Begin(item)`,
`ItemOutput(s)`, `BindItems(s, step, outputs, failed)`;
`ParseCallLabel(label)` читает метку вызова.
`BindingYAML`/`RuleYAML` читают YAML определения (демо, тесты).

### 7.2 Исполнение — Temporal Nexus

Сторона backplane — Nexus-handler, binding-workflow, endpoint'ы — дизайн
сервера (**[backplane]**; в conformance-тестах её играет тест, §16.3);
сторона SDK — вызов хука, worker'ы, активити — описывает код.

Своего транспорта нет. Хук — **Nexus-операция**: Nexus service = сервис
хуков вызывающего (`iam.Hooks`), операция = имя хука (`SendEmail`),
endpoint = имя сервиса (`iam`). Endpoint в реестре Temporal указывает на task queue `backplane`;
backplane — Nexus-handler: находит биндинг, исполняет его как workflow
(шаг = activity или child workflow **по имени** на очереди целевого
сервиса), возвращает результат.

Два уровня данных: **envelope — наш proto** (`backplane.HookCall{hook,
instance, trace, deadline, payload}` на входе Nexus-операции, `hook` —
полное имя `iam.SendEmail`, `trace` — W3C (`traceparent`, `tracestate`,
`baggage`); `backplane.ActivityCall{activity, trace, payload, binding,
step}` на входе активити; симметричные `*Result`); **payload — JSON** автора внутри `bytes`. CEL
работает над payload, envelope не видит. Если тип автора — proto, SDK
кладёт protojson — это тот же JSON, читаемый и в Temporal UI.

- **[backplane]** Endpoint `<service>` backplane создаёт, **как только видит манифест с
  хуками** — не при сохранении биндинга (исполнитель, ниже); вызов без биндинга получает
  `no binding`, а не «endpoint not found». Пока endpoint'а нет (backplane
  ещё не видел сервис), `Call` сразу возвращает `hook.ErrUnavailable`
  («no Nexus endpoint»), не дожидаясь дедлайна.
- Nexus вызывается только из workflow-кода. Внутри workflow —
  `workflows.CallHook(wctx, ref, req)`: Nexus напрямую; дедлайн — `Timeout`
  вызова, иначе `DefaultTimeout` объявления, иначе платформенный
  `hook_timeout`, урезанный до остатка таймаута run'а (§3.4) — всегда
  конечный.
- Вне workflow (HTTP-handler, реактор события, `OnStart`/`OnStop`) —
  `hook.Ref.Call(ctx, req)`: SDK стартует короткий workflow
  `backplane.CallHook.v1` на **очереди хуков** `<service>.hooks`, он делает
  Nexus-вызов и возвращает результат; цена — лишний hop в миллисекунды.
  Id — `hook/<service>/<Name>/<uuid>`, с `hook.Key(k)` —
  `hook/<service>/<Name>/<k>` с политиками `WorkflowIDConflictPolicy:
  UseExisting` (идущий запуск — присоединиться) и `WorkflowIDReusePolicy:
  AllowDuplicateFailedOnly` (успешно завершённый — вернуть его результат,
  упавший — запустить заново). Дедлайн (§3.4) — он же `HookCall.deadline`,
  schedule-to-close Nexus-операции и execution timeout workflow. Memo
  запуска — `source: <service>/<instance>` (§14); search attributes
  `BpService` (`<service>`) и `BpHook` (`<service>.<Name>`) ставятся, если
  в namespace они заведены как Keyword (проверка — один раз на процесс;
  нет или нет прав оператора — вызов идёт без них).
- **Worker хуков.** `backplane.CallHook.v1` обслуживает свой worker — узел
  `hooks` сразу после `temporal`, до дерева автора: стартует раньше и
  останавливается позже него, поэтому `Call` работает из `OnStart` и
  `OnStop` компонентов. У него своя очередь, а не очередь сервиса: worker,
  опрашивающий очередь, валит workflow task'и типов, которых у него нет, —
  на одной очереди живут только worker'ы с одинаковой регистрацией. Worker
  хуков регистрирует только `backplane.CallHook.v1`, есть на каждой
  реплике, объявившей хуки, независимо от `worker.enabled`, и не
  стартует, если хуков нет. Как и основной, он не ждёт Temporal: стартует,
  как только есть соединение.
- Ошибки хука: backplane без биндинга завершает операцию non-retryable
  application error типа `backplane.NoBinding` — автор получает
  `hook.ErrNoBinding` («hook iam.SendEmail: no binding»); прочие отказы —
  сообщение обработчика без обёрток Temporal; истёкший дедлайн —
  `context.DeadlineExceeded`.
- Активити — Temporal activities сервиса на его очереди `<service>`; тип
  activity — имя активити как объявлено (`Send`), вход `ActivityCall`,
  выход `ActivityResult`; регистрирует SDK, декодирование payload в тип
  автора — в SDK; контекст обработчика несёт `activity.InfoOf` и
  `activity.Heartbeat` (§3.5). Ошибка обработчика — с префиксом
  `<service>.<Name>: `, по умолчанию retryable (ретраи — по политике шага
  биндинга); `activity.NonRetryable(err)` — non-retryable (тип
  `backplane.NonRetryable`); `*temporal.ApplicationError` автора
  передаётся как есть; не декодируемый вход — non-retryable. Паника
  обработчика — retryable ошибка `<service>.<Name>: panic: …`, запись в лог
  со стеком и метрика `backplane.panics` (`where: activity
  <service>.<Name>`): паника бывает и от временного состояния, число
  попыток ограничивает шаг. Каждое исполнение —
  `backplane.activity.duration` (`activity`, `outcome`: `ok`/`error`).
- Активити на workflow (`workflows.Activity`, `kind: WORKFLOW`) — workflow
  сервиса на его очереди, тип — имя активити, вход `ActivityCall`, выход
  `ActivityResult`; биндинг исполняет его child workflow'ом по имени.
  Ошибки — те же правила; паника в workflow-коде — как в Temporal: workflow
  task падает и повторяется, run ждёт исправленный worker.
- Trace: OTel-интерцепторы Temporal несут его заголовками через workflow,
  Nexus и activity. Если заголовки не донесли trace, активити продолжает
  trace из `ActivityCall.trace` (span со ссылкой на текущий).
- Temporal недоступен — не ошибка сервиса: SDK подключается в фоне с
  backoff, `Call` до подключения сразу возвращает `hook.ErrUnavailable`,
  worker'ы стартуют, как только есть соединение. Подключённый клиент
  проверяет соединение каждые 5 с; состояние — метрика
  `backplane.transport.connected{transport="temporal"}` (0/1) при каждом
  изменении. Readiness от Temporal по умолчанию не зависит;
  `backplane.RequireTemporal()` делает её зависящей от состояния
  соединения.
- **Раскатка.** Реплики разных версий опрашивают одну очередь сервиса, и
  Temporal отдаёт задачу любой из них:
  - новая активити (или workflow), которую биндинг вызвал, пока жива
    старая реплика: старая получает задачу незарегистрированного типа,
    попытка падает и ретраится по политике шага, пока задачу не возьмёт
    новая реплика. Биндинг на новую активити включают после раскатки или
    дают шагу retry, покрывающий её время;
  - удалённая активити — то же зеркально: снимать её из биндингов до
    раскатки версии без неё;
  - workflow вызова хука версионирован именем, `backplane.CallHook.v1`:
    код, несовместимый при replay, — новое имя (`.v2`), новые вызовы идут
    туда, а `.v1` остаётся зарегистрированным, пока в истории есть его
    незавершённые запуски (они живут не дольше дедлайна вызова). Очередь
    хуков своя: задачи `CallHook` не попадают к worker'ам активити и
    workflows автора, а их раскатка не задевает вызовы хуков;
  - workflows автора (§9) — обычная дисциплина Temporal: изменение,
    ломающее replay, — через `workflow.GetVersion` или новое имя типа.
  Для строгой изоляции версий подходят Worker Versioning / build ID
  Temporal (новая версия берёт только новые запуски, старые дорабатывают на
  старых worker'ах); SDK их не включает — рекомендация для сервисов с
  долгими workflows, настраивается в деплое.

Даром от Temporal: durability (цель лежит — шаг ретраится, сделанные шаги
не переисполняются), компенсации, async-вызов, at-least-once, access
policy endpoint'а, visibility каждого запуска со входом, выходом и шагами.

Цена: Temporal — обязательное ядро; латентность десятки миллисекунд на шаг
(хуки — бизнес-уровень, не hot path); payload до 2 MB.

Ошибки читаемы: `step send: smtp.Send: connection refused`, `no binding
for iam.SendEmail`, `transform failed at step render: <CEL>`.

**Исполнитель — сторона backplane** (`internal/executor`, **[backplane]**):

- **Endpoint'ы.** Для каждого сервиса, у которого хоть один манифест
  реестра (любой версии) объявляет хуки, есть Nexus endpoint `<service>`
  с целью «worker: namespace backplane (`BACKPLANE_TEMPORAL_NS`), очередь
  `backplane`». Исполнитель сверяет их при каждом изменении реестра и раз
  в минуту: нет — создаёт (`AlreadyExists` от другой реплики — успех),
  указывает не туда — переписывает (`UpdateNexusEndpoint` с версией;
  конфликт — следующая сверка). Endpoint'ы этой установки удаляются после
  непрерывного отсутствия инстансов (`BACKPLANE_NEXUS_ABSENCE_GRACE`, 5 минут),
  с повторной согласованной проверкой каталога и instance-state в Consul.
  Unhealthy — не отсутствие, недоступность Consul откладывает удаление.
  Возврат сервиса восстанавливает endpoint; сохранённый манифест без
  инстансов не воссоздаёт удалённый endpoint. Имя сервиса, которое Temporal
  не принимает (оканчивается на `-`), — предупреждение в логе; его хуки недостижимы.

- **Nexus-handler — отдельный worker.** Go SDK Temporal регистрирует
  Nexus-сервисы на worker'е до его старта и диспетчеризует по точному
  имени сервиса и операции — обработчика «на всё» нет, а сервисы с хуками
  появляются в реестре на ходу. Поэтому handler — свой worker исполнителя
  на очереди `backplane`, только Nexus (`DisableWorkflowWorker`,
  `LocalActivityWorkerOnly`: опросчиков workflow- и activity-задач у него
  нет): Nexus-сервис `<service>.Hooks` с операцией на каждый хук, который
  объявляет любой манифест реестра (все версии — старая реплика во время
  раскатки вызывает свои хуки). Набор изменился — стартует новый worker с
  новым набором, затем останавливается старый (задачи в полёте
  дорабатывают). Основной worker backplane (workflows консоли и
  `backplane.Binding.v1`) не перезапускается: его регистрации статичны и
  одинаковы на всех репликах, а Nexus-задачи — отдельный тип задач
  очереди. Набор выводится только из реестра, поэтому реплики сходятся к
  одному; endpoint нового сервиса создаётся после того, как его обслуживает
  локальный worker, и паузы 2 с — за это время изменение реестра доходит
  до других реплик. Остаточный риск — реплика, ещё не увидевшая новый хук,
  получает его вызов и отвечает Nexus `NOT_FOUND`; окно — время
  распространения изменения реестра между репликами. Temporal не подключён
  — исполнитель ждёт (сверка каждые 2 с), на readiness не влияет.
- **Операция** `<Hook>` сервиса `<service>.Hooks`: вход `HookCall`, выход
  `HookResult`. Биндинг в силе (`Manager.Active`) компилируется по
  текущему снапшоту реестра (кэш на версию биндинга и снапшот) и
  запускается как `backplane.Binding.v1` на очереди `backplane`: id
  `binding/<hook>/<Nexus request id>` (повтор старта той же операции — тот
  же запуск), execution timeout — `HookCall.deadline`, memo `source` =
  `HookCall.instance`, `backplane.binding` = `<hook>@<версия>`; операция
  асинхронная — её результат — результат запуска. Нет биндинга:
  обязательный хук (по манифесту версии вызывающего инстанса, иначе
  последнему, иначе любому; хук, которого не объявляет никто, —
  обязательный) — non-retryable application error `backplane.NoBinding`
  «no binding for `<hook>`»; необязательный — синхронный ответ `{}`.
  Биндинг, который больше не компилируется по манифестам, и вход не JSON —
  non-retryable `backplane.HookFailed` с причиной. PostgreSQL или реестр
  не готовы — retryable Nexus-ошибка `UNAVAILABLE`: Temporal повторяет
  старт до дедлайна операции.
- **Workflow `backplane.Binding.v1`** — один на биндинги и правила (§8.1).
  Вход (`executor.Input`, JSON): `kind` (`binding` | `rule`), `program` —
  сериализованный `bindings.Program` (скомпилированное определение едет с
  запуском: сохранения и манифесты после старта его не меняют, replay
  вычисляет ровно скомпилированное), `identity` (`hook` или `rule`,
  `version` — 0 для несохранённого определения, `test`), `payload` — вход
  хука (`req`) или payload события (`event`), `meta` — атрибуты
  CloudEvents правила, `trace` — W3C-контекст вызывающего. Исполнение:
  группы по порядку, шаги группы параллельно; `when` ложно — шаг пропущен
  (`Skip`); шаг — активити **по короткому имени** на очереди
  сервиса-владельца с `ActivityCall{activity: <service>.<Name>, payload:
  Input шага, trace, binding: <hook>@<версия> | rule:<id>@<версия> (`@draft`
  — несохранённое определение), step: <имя шага>}`, для активити `kind:
  WORKFLOW` — child workflow по имени там же (id `<id запуска>/<шаг>`), с
  retry и таймаутами плана шага; выход — `ActivityResult.payload` в `Bind`.
  Группа ждёт все свои шаги, даже после отказа одного, чтобы знать всё,
  что надо компенсировать. Отказ шага (после ретраев) или выражения —
  `undo` выполненных шагов в порядке, обратном завершению, с `UndoInput`
  (в контексте, отключённом от отмены; undo на workflow — id `<id
  запуска>/<шаг>/undo`), затем запуск падает non-retryable: `step <name>:
  <service>.<Activity>: <сообщение обработчика>` (тип
  `backplane.StepFailed`) или `transform failed at step <name> (<место>):
  …` / `transform failed at result…` (тип `backplane.TransformFailed`);
  неудачные undo дописываются в скобках (`(undo charge: billing.Refund:
  …)`). Отмена запуска — те же компенсации, запуск завершается отменённым.
  Результат биндинга — `Result` в `HookResult.payload`; запуск правила
  возвращает пустой `HookResult`. Вычисление — чистые функции `Program`,
  поэтому прямо в коде workflow.
- **Trace.** Handler продолжает trace из `HookCall.trace`, если заголовки
  Nexus его не донесли (старт workflow — в trace вызывающего); шаги
  получают в `ActivityCall.trace` span workflow (интерцептор трассировки),
  без него — `trace` входа.
- **Консоль** (`BindingService`, RPC исполнителя рядом с RPC Manager'а):
  `TestBinding(hook, input, version | definition, timeout)` — текущая,
  указанная или несохранённая версия компилируется по последним манифестам
  (нарушения — в ответе, ничего не запускается) и запускается тем же
  workflow с id `test/<hook>/<uuid>` (identity с `test`, memo `source` —
  сессия консоли); ответ — `CallResult` (выход или ошибка, id запуска) и
  версия; ожидание — таймаут запуска (по умолчанию 30 с, не больше 10 мин)
  плюс 10 с, не дождались — `DEADLINE_EXCEEDED` с id.
  `ListBindingRuns(hook, status, tests, page)` — visibility: `TaskQueue =
  'backplane' AND WorkflowType = 'backplane.Binding.v1' AND WorkflowId
  STARTS_WITH 'binding/<hook>/'` (`test/<hook>/` для тестов), без `ORDER
  BY` (SQL visibility его отвергает; порядок и так — новые первыми).
  `GetBindingRun(workflow_id)` — запуск как `WorkflowService.GetRun`
  (статус, вход, выход или ошибка, история) плюс хук, версия, признак
  теста и **таймлайн шагов**: шаги программы в порядке объявления — вызов
  шага (activity или child workflow: `SCHEDULED`, `STARTED`, `COMPLETED`,
  `FAILED`, `TIMED_OUT`, `CANCELED`; попытка, время, вход, выход, ошибка
  и её тип; для идущих — попытка и последняя ошибка из pending activities)
  или `NOT_RUN` (пропущен `when`, не дошёл, отменён), затем компенсации в
  порядке планирования (второй вызов того же шага — его undo: ретраи —
  попытки одного вызова). `CancelBindingRun(workflow_id)` — запрос отмены.
  Id, не похожий на запуск биндинга, — `INVALID_ARGUMENT`.
- **Метрики** узла `executor`: `backplane.binding.calls{hook, outcome}` —
  старты Nexus-операций (`run`, `default` — `{}` необязательного хука,
  `no_binding`, `error`); `backplane.binding.runs{kind, source, outcome}`
  и `backplane.binding.run.duration` (с) — завершённые запуски (`ok`,
  `failed`, `canceled`; `source` — хук или событие);
  `backplane.binding.step.duration{activity, undo, outcome}` (с) — шаг с
  ретраями (`ok` / `error`); `backplane.executor.hook_services` (gauge) —
  сервисы, которые обслуживает Nexus-worker. Запуски и шаги пишутся из
  кода workflow вне replay: запуск считается один раз, какой бы репликой
  ни закончился.

## 8. События

Тонкая обёртка SDK над брокером; под капотом NATS JetStream. Что обёртка
не покрывает, сервис делает нативным клиентом: `natsdriver.JetStream(scope)`
возвращает `jetstream.JetStream` соединения сервиса (`ErrUnavailable`, если
NATS не сконфигурирован или сервис ещё не стартовал / уже остановлен; пока
соединение разорвано, клиент возвращается и переподключается сам).

- **Стрим на сервис** `bp_<service>`, subjects `bp.<service>.<Event>`
  (фильтр стрима — `bp.<service>.>`); retention и размер изолированы.
  Создаётся **идемпотентно любой стороной** — эмиттером при старте,
  подписчиком или backplane при создании consumer'а; имя и subjects
  выводятся из имени сервиса. Эмиттер (сервис, объявивший хотя бы одно
  событие) при старте делает create-or-update — его конфигурация
  побеждает; подписчик создаёт отсутствующий стрим с дефолтами платформы и
  существующий не трогает. Поэтому стрим есть до того, как эмиттер впервые
  запустился. Всегда: limits-retention, file storage, discard old. Лимиты
  задаёт оператор эмиттера в блоке `nats` (§4.3): `max_age` (7 суток),
  `max_bytes` (без ограничения; при переполнении уходят старейшие
  события), `replicas` (1), `dedup_window` (2 минуты, не больше
  `max_age`); `0` в `max_age`/`max_bytes` — без ограничения. Изменение
  применяется create-or-update при следующем старте эмиттера. Подписчик
  создаёт отсутствующий чужой стрим с дефолтами платформы (7 суток, без
  ограничения размера, 2 минуты, 1 реплика) — эмиттер при старте
  перезапишет их своими; реактор на собственное событие сервиса создаёт
  стрим сразу с лимитами своего блока `nats`. В metadata стрима —
  `bp.service`, `bp.kind` (`events`) и `bp.ensured-by` (`emitter` или
  `subscriber` — кто создал или обновил последним).
- **Имена в NATS.** Имя сервиса, события и consumer'а становится токеном
  NATS так: ASCII-буквы, цифры и `-` остаются, любой другой байт — `_XX`
  (две заглавные hex-цифры). Отображение взаимно однозначно, и в
  экранированном токене не бывает `__`; имена по конвенциям (§17) не
  меняются. Полное имя события `<service>.<Event>` делится по первой точке.
  Имя сервиса `dlq` зарезервировано под dead letters (`Connect` его
  отвергает). Токен `_redrive` экранирование не порождает (escape — `_` и
  две заглавные hex-цифры): он занят subject'ами redrive из консоли
  (`bp.<src>._redrive.<subscriber>.<consumer>`, см. «Redrive»). Имена —
  контракт SDK и сервера, оба берут их из одного пакета `internal/wire`.
- **Событие** — именованное сообщение, payload JSON (proto-тип — как
  protojson); envelope — стандартные заголовки CloudEvents, не наш proto.
  Тип автора — что угодно; SDK сериализует.
  `sent := event.Declare[MailSent](scope, "MailSent", event.Describe("..."))`,
  `sent.Publish(ctx, v, event.Key(k))`; ключ — `subject`. Имя события —
  CamelCase (`[A-Z][A-Za-z0-9]*`), иначе `Declare` паникует;
  `event.Describe` — описание события в манифесте (`events[].description`)
  и консоли. `Ref[*pb.Msg]` декодируется в новый экземпляр сообщения.
- **Совместимость payload.** Схема события меняется только добавлением
  полей. Читатель игнорирует поля, которых не знает (JSON — как
  `encoding/json`, proto — `protojson` с `DiscardUnknown`), поэтому
  эмиттер выкатывает новое поле раньше подписчиков; отсутствующее поле
  читается нулевым значением. Смысл существующего поля не меняется;
  переименование — это новое поле: эмиттер публикует оба, пока все
  реакторы не читают новое, потом старое удаляется. Те же правила — для
  входа и выхода хуков и активити (§3.4, §3.5). Payload, который реактор не
  смог декодировать, — терминальная ошибка (сразу DLQ, см. ниже).
- **Метаданные — CloudEvents** (NATS binding, binary mode, headers
  `ce-*`): `ce-specversion` = `1.0`, `ce-id` (UUID или `event.ID`),
  `ce-source` = сервис, `ce-type` = полное имя события
  `<service>.<Event>`, `ce-time` (RFC 3339 с долями секунды, UTC; момент
  публикации или `event.Time`), `ce-subject` = ключ (если задан), `ce-datacontenttype` и
  `content-type` = `application/json`; расширения `ce-instance`,
  `ce-version` и авторские `event.Header`; контекст трассировки — заголовки
  W3C `traceparent` / `tracestate` (пропагатор OpenTelemetry). `dataschema`
  в v0 не выставляется: схема события — в манифесте. `Nats-Msg-Id` =
  `ce-id`: повтор той же публикации в окне дедупликации JetStream
  отбрасывает.
- **Публикация** ждёт PubAck JetStream в пределах `ctx`; без дедлайна —
  `nats.publish_timeout` (5 с). Своей опции таймаута у `Publish` нет: срок
  задаёт `ctx` вызывающего. Пока NATS недоступен, `Publish` сразу
  возвращает ошибку (`ErrUnavailable`): SDK не буферизует и не ждёт
  переподключения. Если собственного стрима нет, SDK создаёт его и
  повторяет публикацию один раз. Опции публикации:

  | Опция | Смысл |
  |---|---|
  | `event.Key(k)` | `ce-subject`, ключ события |
  | `event.ID(id)` | `ce-id` и `Nats-Msg-Id` = `id` вместо UUID: outbox, публикующий строку повторно под её id, в окне `dedup_window` публикует её один раз; пустой id — ошибка |
  | `event.Time(t)` | `ce-time` — когда событие произошло; нулевое время — момент публикации |
  | `event.Header(name, v)` | расширение CloudEvents `ce-<name>`; `name` — строчные латинские буквы и цифры, атрибуты SDK (`id`, `source`, `type`, `time`, `subject`, `datacontenttype`, `dataschema`, `specversion`, `instance`, `version`) и значение с переводом строки не принимаются |

  Недопустимое значение опции — `Publish` возвращает `event.ErrOption`,
  ничего не отправив.
- **Подключение** не блокирует старт: недоступный NATS — предупреждение,
  клиент переподключается бесконечно (пауза 2 с плюс jitter до 1 с), стрим
  эмиттера и consumers реакторов досоздаются в фоне с backoff; разрывы и
  переподключения пишутся в лог и в метрику
  `backplane.transport.connected{transport="nats"}`. `BACKPLANE_NATS_CREDS`
  — содержимое `.creds`-файла (JWT и seed пользователя). TLS — блок
  `nats.tls` (§4.3: `enabled`, PEM-содержимое `ca`, `cert`, `key`,
  `server_name`, `insecure_skip_verify`; пустой `ca` — системный пул;
  `cert` и `key` — только вместе; ошибка в них — ошибка старта узла
  `nats`). На остановке соединение дренируется в бюджете остановки. На
  readiness NATS по умолчанию не влияет.
- **Порядок.** JetStream хранит события в порядке публикации, партиций
  нет. Реактор — durable consumer, **общий для всех реплик сервиса**:
  реплики — конкурирующие потребители, каждое событие получает одна из
  них. Поэтому при нескольких репликах или `Concurrency > 1` события
  обрабатываются параллельно и порядок, в том числе внутри одного ключа,
  не гарантирован; упавшее событие к тому же возвращается после задержки
  позади следующих. Строгий порядок — `event.Ordered()`: `max_ack_pending`
  consumer'а = 1 и `Concurrency` 1, так что во всём сервисе в полёте одно
  событие этого реактора; упавшее событие держит следующие, пока не
  пройдёт или не уйдёт в DLQ. Цена — пропускная способность одного вызова
  обработчика с сетевым круговым путём, сколько бы реплик ни было.
- **Реакторы**: `event.React[UserRegistered](scope, "iam.UserRegistered",
  handler, opts...)` = durable pull consumer, ack/nak, redelivery,
  `max_deliver` → DLQ. Имя события — `<service>.<Event>` (`[a-z0-9-]+`,
  точка, CamelCase), иначе `React` паникует. Consumer назван по пути узла и
  событию (`<путь узла>:<событие>`, на `Root` — само имя события) и уникален
  в сервисе: у каждого реактора своя позиция, несколько реакторов на одно
  событие не мешают друг другу. **Переименование или перенос компонента
  меняет имя consumer'а**: новый начинает по `StartAt`, старый остаётся на
  сервере со своей позицией (его удаляет оператор или `InactiveThreshold`).
  `event.Consumer(name)` закрепляет имя явно — тогда компонент можно
  переименовывать; чтобы сохранить существующий consumer, закрепляют его
  текущее имя. Манифест перечисляет реакторы в `subscriptions` (`event`,
  `consumer`); durable consumer в NATS — `<subscriber>__<consumer>` (оба
  экранированы, см. «Имена в NATS»; длиннее 200 символов — обрезается и
  дополняется хешем), на стриме `bp_<src>` с двумя фильтрами:
  `bp.<src>.<Event>` и redrive-subject реактора
  `bp.<src>._redrive.<subscriber>.<consumer>` (его не матчит ни один другой
  consumer); в metadata consumer'а — `bp.service`, `bp.consumer`,
  `bp.event`. Фильтры пишутся create-or-update при каждом старте, так что
  consumer, созданный SDK без redrive-subject'а, получает его при
  следующем старте сервиса.
  Consumer, удалённый на сервере, пока сервис работает, SDK пересоздаёт
  с позиции реактора — со старейшего неподтверждённого сообщения, иначе
  сразу после последнего увиденного — и потребление продолжается.
  Новый consumer начинает с событий, опубликованных после его создания
  (`deliver_policy new`), или, с `event.StartAt(event.StartAll)`, со всех
  событий, которые стрим ещё хранит (`deliver_policy all`); дальше позиция
  хранится в NATS. Точка старта действует только при создании:
  существующий consumer сохраняет свою позицию, и `StartAll`, добавленный
  работающему реактору, ничего не переигрывает (для повтора истории —
  новый consumer, т. е. другое имя, или удаление consumer'а оператором).
  Подписчик декодирует своим типом (копия схемы) или динамически. Читать
  чужие стримы может любой; «подписан на всё» = consumer на каждый стрим из
  каталога манифестов, новые — по мере появления. Ограничения доступа, если
  нужны, — правами NATS-пользователя.
- **Доставка реактору** — at-least-once: обработчик может получить то же
  событие повторно и должен быть идемпотентным (`Delivery.ID` одинаков во
  всех доставках). Explicit ack; обработчик получает контекст с трассой
  события, таймаутом и метаданными: `event.DeliveryOf(ctx)` → `Delivery`
  (`ID`, `Source`, `Type`, `Subject` — ключ, `Time`, `Attempt` — номер
  доставки с 1, `Consumer` — имя из манифеста, `Extensions` — остальные
  `ce-*` без префикса: `event.Header` эмиттера, `instance`, `version`).
  Паника — ошибка (и `backplane.panics{where="reactor"}`). Успех — ack.
  Ошибка — nak с задержкой, удваивающейся с каждой доставкой от нижней
  границы до верхней; неуспешная последняя доставка — DLQ. Ошибка,
  обёрнутая `event.Terminal(err)`, — DLQ сразу, без повторов: вход, который
  повтор не исправит; payload, который не декодируется, — тоже терминальная
  ошибка. `ack_wait` = таймаут обработчика + 15 с — через столько
  возвращается сообщение, которое никто не подтвердил (процесс упал).
  Свой `BackOff` consumer'а не задан: сервер отсчитывает задержку nak от
  `ack_wait`, и вместе с `BackOff` задержки расходились бы. Решение о DLQ
  принимает SDK по номеру доставки; `max_deliver` consumer'а — опция
  `MaxDeliver` + 5 доставок запаса на прерванные остановкой (см. ниже), так
  что сообщение, прерванное на последней доставке, возвращается. Сообщение,
  которое ни разу не подтвердили (процесс падает на нём каждый раз), сервер
  перестаёт доставлять после `MaxDeliver` + 5 доставок; в DLQ оно не
  попадает — остаётся только в стриме события. Consumption переживает
  переподключения NATS. Метрика `backplane.reactor.duration{consumer,
  outcome}`: `ack`, `nak` (повтор или прерванная остановкой доставка),
  `dead` (последняя доставка), `terminal`.
- **Опции реактора** — решение автора кода, в `event.React(..., opts...)`;
  бессмысленное значение — паника при объявлении (как у `deps.Backoff`):

  | Опция | По умолчанию | Смысл |
  |---|---|---|
  | `event.MaxDeliver(n)`, `n ≥ 1` | 5 | доставок одного сообщения, первая включительно; `1` — без повторов, первая ошибка — сразу DLQ |
  | `event.Concurrency(n)`, `n ≥ 1` | 4 | обработчиков одновременно на реактор в одном инстансе (и размер выборки); `1` — по одному в инстансе, но другие реплики работают параллельно, а упавшее сообщение вернётся после задержки позади следующих |
  | `event.Ordered()` | выкл. | строгий порядок во всём сервисе: `max_ack_pending` 1, `Concurrency` 1 (см. «Порядок»); с `Concurrency(n > 1)` — паника |
  | `event.Timeout(d)`, `d > 0` | 30 с | таймаут одного вызова: контекст отменяется, вызов считается неуспешным; `ack_wait` = `d` + 15 с |
  | `event.Redelivery(min, max)`, `0 < min ≤ max` | 1 с..1 мин | задержка перед повторной доставкой: `min` после первой ошибки, удваивается до `max` |
  | `event.StartAt(event.StartNew\|StartAll)` | `StartNew` | откуда начинает **новый** consumer (см. выше) |
  | `event.Consumer(name)`, непустое | `<путь узла>:<событие>` | имя consumer'а, уникальное в сервисе (см. выше) |
  | `event.InactiveThreshold(d)`, `d > 0` | нет | сервер удаляет consumer, если `d` его не тянет ни один инстанс (убранный или переименованный реактор); удалённый так consumer при следующем старте создаётся заново по `StartAt`, события за время простоя при `StartNew` пропускаются |

  `max_deliver`, `ack_wait`, `max_ack_pending` и `inactive_threshold`
  пишутся в конфигурацию consumer'а при каждом старте реакторов
  (create-or-update), так что изменённые опции действуют после выкатки;
  точка старта — только при создании. В манифест опции не попадают: их
  видно в конфигурации consumer'а в NATS. В `backplanetest` `React`
  вызывает обработчик один раз с `Timeout` реактора и `Delivery` на
  контексте; повторов и DLQ там нет (§4.6).
- **DLQ.** Если последняя доставка (`MaxDeliver`) тоже неуспешна или
  ошибка терминальна, сообщение с исходными payload и заголовками (кроме
  служебных `Nats-*`) публикуется в `bp.dlq.<subscriber>.<consumer>` —
  стрим `bp_dlq_<subscriber>` (subjects `bp.dlq.<subscriber>.>`; подписчик
  создаёт или обновляет его при старте реакторов; хранение — `dlq_max_age`
  блока `nats`, 30 суток, `0` — без ограничения; реплики — `replicas`;
  окно дедупликации — 2 минуты, не больше хранения; metadata `bp.kind` =
  `dead-letters`) — с
  заголовками `bp-error` (текст последней ошибки, до 4 KiB), `bp-consumer`
  (имя consumer'а из манифеста) и `bp-delivered`; `Nats-Msg-Id` =
  `<durable>:<стрим>:<seq>`, так что повтор не дублирует dead letter.
  Исходное сообщение после этого терминируется (term).
- **Redrive.** `event.Redrive(ctx, scope, consumer)` прогоняет dead
  letters реактора `consumer` (имя из манифеста) через его же обработчик
  в вызвавшем инстансе, от старых к новым, с `Timeout` реактора и
  `Delivery` (`Attempt` = `bp-delivered` + 1); обработанный dead letter
  удаляется, снова упавший остаётся на месте. Dead letters, пришедшие во
  время прогона, остаются до следующего вызова. Возвращает число
  обработанных и ошибку, если какие-то упали снова. В исходный стрим
  события не публикуются заново: иначе их получили бы все подписчики
  события, а не один реактор, и повторный `ce-id` съела бы дедупликация.
  Вызывается одним инстансом (из внутреннего API или админ-команды
  сервиса): два параллельных прогона могут обработать dead letter дважды.
- **[backplane] Redrive из консоли.** backplane обработчики сервиса не
  исполняет, поэтому `EventService.RedriveDeadLetters` возвращает dead
  letter реактору через NATS: публикует его payload и заголовки (без
  `Nats-*`, `bp-error`, `bp-consumer`, `bp-delivered`; плюс `bp-redriven` =
  sequence dead letter'а) в redrive-subject реактора
  `bp.<src>._redrive.<subscriber>.<consumer>` — он внутри стрима источника
  `bp_<src>`, и на него фильтрует только consumer этого реактора (JetStream
  не фильтрует consumer'ы по заголовкам, а consumer читает только свой
  стрим). После PubAck dead letter удаляется из `bp_dlq_<subscriber>`.
  Реактор получает сообщение как новую доставку: `Attempt` с 1, весь
  `MaxDeliver` заново, `ce-id` прежний (идемпотентный обработчик его
  узнаёт); снова упавшее становится новым dead letter'ом. `Nats-Msg-Id` =
  `redrive:<durable>:<seq>`: повтор redrive того же dead letter'а в окне
  дедупликации не дублирует. Другие подписчики события redrive не видят.
  Предусловия (иначе `FAILED_PRECONDITION`): consumer реактора есть на
  сервере и среди его фильтров есть redrive-subject (сервис на SDK без
  него — только `event.Redrive` в самом сервисе). Права NATS backplane —
  publish в `bp.*._redrive.>`, чтение и удаление сообщений `bp_dlq_*`.
- **Остановка реакторов** (до остановки дерева автора): выборка
  прекращается, выбранные, но не начатые сообщения возвращаются (nak), SDK
  ждёт обработчиков в полёте до конца общего бюджета остановки (своего
  окна у узла `reactors` нет, §4.4); по его концу их контексты
  отменяются, а остановка узла — ошибка `Run`. Обработчик, прерванный
  остановкой, не считается упавшим: сообщение возвращается простым nak
  без задержки и никогда не уходит в DLQ. Такой nak уходит, когда бюджет
  уже исчерпан, и соединение NATS к этому моменту может быть закрыто —
  тогда сообщение возвращается по `ack_wait` (таймаут обработчика +
  15 с), тоже без DLQ. Счётчик доставок NATS такая доставка всё же увеличивает
  (сервер не отличает её от обычной): `Attempt` у следующей доставки на 1
  больше, а число настоящих попыток до DLQ может стать меньше
  `MaxDeliver`, но DLQ всегда следует за настоящей ошибкой обработчика.
- Метрика публикаций — `backplane.event.published{event, outcome}`:
  `ok`, `unavailable`, `timeout`, `error`.
- Интерфейс SDK узкий и брокеро-независимый: publish, durable subscribe,
  ack/nak, DLQ и его redrive; повтор истории — новый consumer со
  `StartAll`. Kafka под него встаёт; v0 — NATS.
- **[backplane]** backplane в data path событий не участвует; наблюдает
  каталог, lag, DLQ; держит consumers правил (§8.1). Консоль — сервис
  `EventService` (`internal/ops`, NATS — соединение backplane через
  `natsdriver.JetStream`, `BACKPLANE_NATS_URL`; без него NATS-вызовы —
  `UNAVAILABLE`, а `ListEvents` отдаёт одни объявления с `nats_error`):
  - `ListEvents` — все события из последних манифестов: схема,
    описание, subject, число сообщений события в стриме, последнее
    (seq, время); подписчики — реакторы сервисов (`subscriptions`),
    consumers правил (`backplane__*`) и прочие consumers стрима, у каждого
    состояние consumer'а (`num_pending` — lag, `num_ack_pending`,
    `num_redelivered`, последняя доставка и ack, пауза, фильтры, политики,
    metadata), число dead letters и умеет ли он redrive; стримы сервисов
    (сообщения, байты, первый/последний seq и время, лимиты, `bp.ensured-by`);
  - `GetStream(service)` — стрим событий и стрим dead letters сервиса, все
    consumers стрима событий, dead letters по реакторам;
  - `PeekMessages(service, event?, start_seq | start_time, limit)` —
    страница вперёд от sequence или времени, без старта — последние
    `limit` (по умолчанию 50, не больше 500); сообщение — seq, subject,
    время записи, все заголовки, разобранные `ce-*`, payload JSON-текстом
    (не JSON — байтами), пометка теста;
  - `PublishTestEvent(event, payload, key?, extensions?, id?)` — только
    объявленное событие, payload — валидный JSON (пустой — `{}`); это
    событие сервиса: `ce-source` — сервис, `ce-type` — полное имя, как
    опубликовал бы эмиттер, чтобы подписчики читали его как обычное;
    пометки консоли — `ce-instance` = `backplane`, `ce-bptest` = `true`,
    `ce-bpauthor` = автор; `Nats-Msg-Id` = `ce-id` (повтор с тем же `id`
    в окне дедупликации — `duplicate`); нет стрима — `FAILED_PRECONDITION`;
  - `ListDeadLetters(subscriber, consumer?, start_seq, limit)` — dead
    letters от старых к новым с `bp-error`, `bp-consumer`, `bp-delivered`
    и subject'ом исходного события, счётчики по реакторам;
  - `RedriveDeadLetters(subscriber, consumer, seqs | all)` — см. «Redrive
    из консоли»; ответ — сколько возвращено и отказы по seq;
  - `PurgeDeadLetters(subscriber, consumer?, seqs?)` — удалить выбранные,
    все dead letters реактора или сервиса.

### 8.1 Правила: событие → активити [backplane]

Правило — **биндинг, у которого источник — событие, а не хук**. Та же
модель (§7.1), тот же исполнитель, та же консоль (Wiring):

```yaml
event: iam.UserRegistered
when: event.email != ""
steps:
  render:
    activity: template.Exec
    input: {name: "'welcome'", data: {name: event.name}}
  send:
    activity: smtp.Send
    input: {to: event.email, subject: render.subject, text: render.text}
```

Реакция на событие по многим получателям — цикл (§7.1): удалили
пользователя — каждый его ресурс освобождается своей активити, упавшие
не мешают остальным:

```yaml
event: iam.UserDeleted
steps:
  release:
    forEach: event.resources        # [{service, id}]
    as: resource
    onError: continue
    activity: storage.Release
    input: {owner: event.id, id: resource.id}
  report:
    when: steps.release.failed > 0
    activity: ops.Notify
    input: {text: "'release failed: ' + steps.release.errors.map(e, e.message).join(', ')"}
```

Отличия от биндинга хука: вход называется `event` (payload события), к
нему `meta` — атрибуты CloudEvents (`id`, `source`, `subject`, `type`,
`time` — строки, `time` в RFC 3339); `result` нет.

**Модель** (proto `backplane.console.v1.RuleDefinition`, Go
`internal/bindings.Rule`): `event` — полное имя `<service>.<Event>`;
`when` — CEL bool над `event` и `meta` (шаги ему не видны), пусто —
всегда; `steps` — шаги §7.1; `description`, `editor` — как у биндинга.
Правило — uuid; его `name` версионируется вместе с определением, пауза
(`paused`) — флаг вне версий.

**Валидация** — как у биндинга (§7.1): событие объявлено в последнем
манифесте сервиса, `event` типизирован его схемой; у правила обязательно
непустое имя. **Версии** — `rule` (id, paused), `rule_version` (rule_id,
version с 1, name, `definition` — protojson `RuleDefinition`, NULL —
надгробие, author, comment, created_at, rollback_of), `rule_current`;
сохранение, откат, удаление и `base_version` — как у биндинга (у паузы и
возобновления — тоже: пауза относится к версии, которую видел оператор).
Состояние правила: `ACTIVE`, `PAUSED`, `DELETED`, `BROKEN` — текущее
определение больше не компилируется по последним манифестам (важнее
паузы), `Rule.violations` говорят, где. Действуют правила не удалённые, не
на паузе и компилирующиеся.

**Консоль** — `RuleService`: `ListRules` (все или одного события, по
имени), `GetRule`, `ListRuleVersions`, `ValidateRule`, `SaveRule` (без
id — новое правило), `RollbackRule`, `DeleteRule`, `PauseRule`,
`ResumeRule`, `WatchRules`; запуски — RPC
движка правил в том же сервисе: `TestRule`, `ListRuleRuns`, `GetRuleRun`,
`CancelRuleRun` (см. «Запуски в консоли»).

**Для движка правил** (`internal/bindings`): `Manager.ActiveRules(ctx)` —
действующие правила, `Manager.Rules(ctx)` — все (с удалёнными и на
паузе), `Manager.Changes(ctx)`; `CompileRule(r, catalog)` — `*Program`;
`Program.StartEvent(payload, Meta)` — `Scope` события, `Match(s)` —
`when`; шаги — как у биндинга.

**Исполнение** — движок правил `internal/rules`, компонент `rules` в
дереве backplane.

- **Consumer на правило.** На каждое правило не удалённое и не на паузе
  backplane держит durable consumer на стриме события: стрим `bp_<svc>`,
  фильтр `bp.<svc>.<Event>`, имя `backplane__rule-<id>`
  (`wire.Durable("backplane", "rule-<id>")`, id — uuid правила), explicit
  ack, `ack_wait` 1 мин, `max_deliver` без ограничения; в metadata —
  `bp.service` = `backplane`, `bp.consumer` = `rule-<id>`, `bp.event`,
  `bp.rule` = id и `bp.rule-version` — версия, последней записавшая
  consumer. Consumer общий для всех реплик backplane — конкурирующие
  потребители; в одной реплике до 8 доставок правила обрабатываются
  одновременно. Отсутствующий стрим события движок создаёт с дефолтами
  платформы и существующий не трогает — как подписчик SDK (§8).
- **Точка старта.** Новый consumer начинает с событий, опубликованных
  после его создания (`deliver_policy new`): сохранённое правило не
  переигрывает историю стрима (иначе, например, приветственные письма
  ушли бы всем старым пользователям). Существующий consumer сохраняет
  позицию.
- **Событие.** Заголовки CloudEvents (`ce-*`) становятся `meta`; `ce-id`
  обязателен, `ce-time` не в RFC 3339 читается как пустое время. `when`
  вычисляется до старта: не подошло — ack без запуска. Подошло —
  `ExecuteWorkflow` исполнителя (`backplane.Binding.v1` на очереди
  `backplane`) со входом вида `rule`: скомпилированная программа, payload
  события, `meta`, правило (id, версия) и контекст трассировки события
  (`traceparent`, `tracestate`); memo `source` = `rule:<id>`,
  `backplane.binding` = `rule:<id>@<версия>`. **Workflow id =
  `rule/<id>/<ce-id>`**, политика «дубликат отклонить»: повторная доставка
  или повторная публикация того же `ce-id` находит свой запуск и
  подтверждается без второго (в пределах retention namespace'а Temporal).
- **Ack после старта**: durability с этого момента у Temporal, DLQ на
  уровне NATS правилам не нужен. Старт не удался (Temporal недоступен) —
  nak с задержкой от 1 с, удваивающейся до 1 мин, без ограничения числа
  доставок: события ждут в стриме. Старт, прерванный остановкой
  backplane, — простой nak. Событие без `ce-id`, payload не JSON или
  `when`, упавший на нём, — term и предупреждение в логе: повтор такое
  событие не исправит.
- **Сверка** — при изменении правил (`Manager.Changes`), манифестов
  (реестр) и раз в минуту: новое действующее правило получает consumer;
  новая версия (или изменившийся из-за манифестов скомпилированный
  шаблон) переписывает consumer (фильтр, metadata) с сохранением позиции,
  следующие события вычисляются новой версией; пауза прекращает чтение и
  оставляет consumer — события копятся на его позиции, возобновление
  продолжает с неё; правило, которое больше не компилируется (событие или
  активити пропали из манифестов), тоже перестаёт читать, consumer
  остаётся, причина — в логе; удалённое правило — consumer удаляется;
  событие правила переехало в стрим другого сервиса — старый consumer
  удаляется, новый начинает с новых событий. Consumer правила, удалённый
  на сервере вручную, создаётся заново с новых событий. Consumers правил,
  которые реплика не читает, сверка удаляет, только если правило удалено
  или живёт на другом стриме и версия в metadata consumer'а не новее
  известной реплике — отстающая реплика не удаляет consumer, созданный
  более новой версией.
- Правила **не упорядочены**, запуски параллельны. Если порядок важен —
  это реактор внутри сервиса, не правило.
- Без NATS или Temporal движок пишет в лог, почему правила не работают, и
  ждёт; на readiness backplane не влияет.
- Метрики: `backplane.rules.messages{rule, outcome}` — доставки по исходу
  (`skipped` — `when` ложно, `started`, `dedup` — запуск уже есть,
  `failed` — старт не удался, `invalid` — term), `backplane.rules.matched{rule}`
  — доставки с истинным `when`.

**Запуски в консоли** (`RuleService`):

- `TestRule(id [+ version] | definition, event, meta?, dry_run?,
  timeout?)` — определение компилируется по последним манифестам
  (нарушения — в ответе), `when` вычисляется на примере события (`meta`
  по умолчанию: новый uuid, сервис и полное имя события, текущее время);
  подошло и не `dry_run` — тестовый запуск `test/rule/<id>/<uuid>`
  (`test/rule/draft/<uuid>` для несохранённого определения, identity с
  `test`) и ожидание результата (по умолчанию 30 с, не больше 10 мин;
  не дождались — `DEADLINE_EXCEEDED` с id запуска);
- `ListRuleRuns(id, status?, tests?)` — запуски по префиксу workflow id
  `rule/<id>/` (с `tests` — `test/rule/<id>/`) через visibility, как
  `WorkflowService.ListRuns` (без `ORDER BY`);
- `GetRuleRun(id, workflow_id, run_id?)`, `CancelRuleRun` — детали
  (с таймлайном шагов, как у биндинга) и отмена запуска, только запусков
  этого правила (`NOT_FOUND` для чужого id); пустой `id` — тестовый запуск
  несохранённого определения (`test/rule/draft/…`).
- На карточке события — правила; на карточке правила — запуски и тест.

## 9. Workflows

Сервис описывает свои workflows и activities как хочет; с proto —
`protoc-gen-go-temporal` даёт стабы и типизированные клиенты. Task queue =
имя сервиса, namespace один (`BACKPLANE_TEMPORAL_NS`, по умолчанию
`default`). Драйвер SDK даёт подключение, worker и настройку worker'а
из конфигурации; больше ничего своего. Логи Temporal SDK идут в логгер
сервиса, метрики и трейсы — в OTel сервиса.

- внутри сервиса — обычный Temporal: retry, таймеры, сигналы, расписания;
- между сервисами — только через хуки (§7): activity вызывает `Call` хука,
  реализация — биндинг; чужих очередей и типов сервис не знает;
- **[backplane]** из консоли — запустить любой workflow, хук или активити с входом: форма
  по схеме, если есть, иначе JSON; запуски, история, отмена —
  Temporal API в карточке. Сервисы консоли (`internal/ops`; Temporal —
  клиент backplane через `workflows.Client`, `BACKPLANE_TEMPORAL_ADDR`;
  без него — `UNAVAILABLE`); всё, что консоль запускает, несёт memo
  `source` = `console:<session>` (§14):
  - `WorkflowService`: `ListWorkflows` — `workflows[]` последних
    манифестов и активити вида `WORKFLOW` со схемами, очередью и числом
    worker'ов, недавно опрашивавших её (`DescribeTaskQueue`; не ответил
    Temporal — не задано);
    `StartWorkflow(service, workflow, input, workflow_id?, timeout?)` —
    только объявленный, на очереди сервиса, вход — JSON как есть (активити
    на workflow — завёрнутым в `ActivityCall`), id по умолчанию
    `console/<service>/<workflow>/<uuid>`; id, у которого есть идущий
    запуск, — `ALREADY_EXISTS` (закрытый — новый запуск); `ListRuns(service,
    workflow?, status?, workflow_id_prefix?, hooks?, page)` — visibility-запрос
    `TaskQueue = '<service>' [AND WorkflowType …] [AND ExecutionStatus …]
    [AND WorkflowId STARTS_WITH …] ORDER BY StartTime DESC` (кавычки в
    значениях отвергаются; запуски расписания — префикс
    `<service>/<Name>-`; с `hooks` вместо очереди сервиса — его вызовы хуков:
    очередь `<service>.hooks` и консольные `hook/<service>/…` на очереди
    backplane); SQL visibility (dev-сервер, PostgreSQL, MySQL) отвергает
    `ORDER BY` — тогда запрос без него, порядок Temporal (идущие, затем по
    времени закрытия), ответ несёт выполненный запрос; `GetRun(workflow_id,
    run_id?)` — статус, memo, родитель (workflow id и run id), вход, результат
    или ошибка с типом, run id продолжения (continue-as-new), pending
    activities, история таймлайном (тип события, одна строка сводки, payload
    JSON-текстом — у конвертов `ActivityCall`/`ActivityResult`/`HookCall`/
    `HookResult` байты `payload` показаны JSON, который они несут; ошибка;
    связанный запуск — дочерний или продолжение; до 1000 событий,
    закрывающее — всегда); `CancelRun`, `TerminateRun(reason)` (к причине
    дописывается автор), `SignalRun(signal, input)` (пустой вход — сигнал без
    аргумента). Действия над запуском принимают любой workflow id
    namespace'а: запуски биндингов и правил живут на очереди backplane, а
    сессия консоли — оператор установки;
  - `ScheduleService`: `ListSchedules` — сохранённые расписания Temporal
    с префиксом `<service>/`, включая отсутствующие сервисы. `GetSchedule`,
    `CreateSchedule`, `UpdateSchedule` с ревизией и `DeleteSchedule` —
    административное управление. Вход проверяется по схеме workflow,
    обычное обновление сохраняет операторскую паузу;
    живое состояние — пауза и note, ближайшие запуски (до 5), недавние
    (время, workflow id, run id), идущие, счётчики, владелец из memo
    `backplane.service`, тип workflow; `PauseSchedule`/`UnpauseSchedule`
    (note + автор) и `TriggerSchedule` (Temporal не принимает memo на
    trigger: запуск несёт memo действия расписания, кто нажал — в аудите).
    Ручную паузу меняет только административная команда, SDK её не трогает;
  - `CallService`: вызовы идут коротким workflow на **очереди backplane**
    `backplane` (зарегистрирован через `workflows.Register` сервиса
    backplane; результат, вход и ошибка — в Temporal как у любого
    запуска), API ждёт результат. `CallHook(hook, input, key?, timeout?)`
    — `backplane.console.CallHook.v1` делает Nexus-вызов, как сделал бы
    сервис: операция `<Name>` сервиса `<service>.Hooks` на endpoint'е
    `<service>`, `HookCall{hook, instance: console:<session>, payload,
    trace, deadline}`; дедлайн — из запроса, иначе объявленный `timeout`
    хука, иначе 30 с (не больше 10 мин); id
    `hook/<service>/<Name>/console-<uuid>`, с ключом —
    `hook/<service>/<Name>/<key>` с теми же политиками, что у `hook.Key`:
    консоль и сервис с одним ключом попадают в один запуск; нет
    Nexus-endpoint'а — `FAILED_PRECONDITION`. `RunActivity(activity, input,
    start_to_close?, max_attempts?, step?)` —
    `backplane.console.RunActivity.v1` исполняет активити по имени на
    очереди сервиса с `ActivityCall{activity, payload, binding: console,
    step}`: activity или, для вида `WORKFLOW`, child workflow;
    start-to-close — из запроса, иначе объявленный, иначе 1 мин; попыток
    — `max_attempts`, по умолчанию одна. Ответ обоих — `CallResult`:
    выход JSON-текстом или ошибка обработчика с типом application error
    (`backplane.NoBinding`, `backplane.HookFailed`, `backplane.Timeout`,
    `backplane.NonRetryable`), workflow id, run id, длительность; не
    дождались — `DEADLINE_EXCEEDED` с id запуска.

**Регистрация и клиент.** `workflows.Register(scope, func(r
worker.Registry))` добавляет workflows и activities автора в worker
сервиса; объявление — до `Run`, после него — паника.
`workflows.Client(scope)` — Temporal-клиент сервиса (пока Temporal не
задан или не подключён — ошибка с `hook.ErrUnavailable`),
`workflows.Queue(scope)` — его task queue. Запуск своего workflow —
`ExecuteWorkflow` клиента на `Queue`.

**Объявленные workflows.** `workflows.Declare[In, Out](scope, name, fn,
workflows.Describe(s))` регистрирует `fn func(workflow.Context, In) (Out,
error)` в worker сервиса под типом `name` и записывает его в манифест
(`workflows[]`: имя, схемы входа и выхода, описание) — консоль запускает
его формой по схеме. Имя — CamelCase (§3.5), неуникальное — ошибка `Run`
(`ErrDuplicate` манифеста), объявление после `Run` — паника. Вход
приходит через конвертер Temporal по умолчанию: JSON, protojson для
proto. Объявленный через `Declare` workflow в `Register` не повторяют —
двойная регистрация в Temporal — паника.

**Worker** (identity = id инстанса) несёт activities и workflows автора и
активити сервиса (§3.5, обоих видов); создаётся, если сервис объявил
что-то из этого. Стартует после дерева автора, как только есть
соединение (неудачный старт повторяется с backoff), и останавливается до
него: `Stop` перестаёт брать задачи и ждёт идущие активити в пределах
бюджета остановки. Вызовы хуков вне workflow он не несёт: у них свой
worker на очереди `<service>.hooks`, живущий дольше дерева автора (§7.2).
Настройка — эксплуатационная, из блока `backplane.temporal.worker` (env
`BACKPLANE_TEMPORAL_WORKER_*`, §4.3), код тот же при любой:

| поле | смысл |
|---|---|
| `enabled` | `false` — реплика без worker'а: очередь сервиса обслуживают другие реплики (отдельный деплоймент-воркер), а эта держит клиент, `Call` хуков (worker хуков работает и здесь) (по умолчанию `true`) |
| `max_concurrent_activities` | activities, исполняемых одновременно |
| `max_concurrent_workflow_tasks` | workflow task'ов одновременно |
| `activity_pollers`, `workflow_pollers` | поллеры очередей activity и workflow task'ов |

`0` — умолчание Temporal SDK; отрицательное — ошибка `Open`.

**Раскатка.** Во время раскатки реплики старой и новой версии опрашивают
одну очередь сервиса; задача достаётся любой. Workflow автора, изменённый
несовместимо с replay, — через `workflow.GetVersion` или новое имя типа;
новый тип (workflow, активити) старые реплики не знают — их попытки
падают и ретраятся, пока задачу не возьмёт новая реплика, поэтому новые
типы начинают вызывать после раскатки (правила для биндингов и
`backplane.CallHook.v1` — §7.2). Для долгих workflows рекомендуется Worker
Versioning / build ID Temporal в деплое; SDK его не включает.

**Расписания — административная функция сервера.** SDK регистрирует
workflow и выполняет задания из очереди, но не объявляет расписания,
не создаёт, не обновляет и не удаляет их при старте или остановке реплики.
Ни обычный рестарт, ни rolling update не меняют операторскую паузу.

Создание, редактирование и удаление расписаний принадлежат административному
API Backplane. Это следующий серверный этап: текущий API уже умеет список,
паузу, снятие паузы и ручной запуск; CRUD ещё не реализован. До него
существующими расписаниями управляют через Temporal. Нативный Temporal
клиент доступен явно через `workflows.Client`, но SDK не выполняет с ним
никаких автоматических операций над расписаниями.

Существующие расписания сохраняются при обновлении SDK. Для перехода
нужно обновить все реплики: старый SDK продолжает свою сверку до остановки.
Поле `Manifest.schedules` сохранено для чтения манифестов старых версий;
новый SDK его не заполняет. Административные изменения нельзя основывать
на этом поле или на объявлениях отдельной реплики.

## 10. Прямые вызовы и mesh

Для чужих сервисов, не наших модулей: резолв `courier.service.consul` через
Consul DNS и обычный gRPC/HTTP. Ничего между ними нет; backplane не
участвует. Service mesh (sidecar, intentions, mTLS) не требуется — это
инфраструктура деплоя, её нет вне k8s. Чтобы установке с mesh ничего не
мешало: регистрация в Consul — не наша монополия (§4.2); SDK не резолвит
чужие адреса; backplane не вызывает сервисы по адресу иначе как relay
консоли; в mesh gateway-драйвер — Consul config entries (§6).

## 11. Консоль [backplane]

Раздел — сервер и фронтенд платформы; сторона SDK — секрет платформенного
порта (§4.1, §13) и отдача бандла (§3.6).

Shell — собственный UI backplane. Два слоя:

- **Карточки** — из манифеста и стека, без кода сервиса: инстансы и health
  (Consul), конфигурация по инстансам с источниками (обычные поля
  read-only, Live — редактируемые, с историей, §5), дерево узлов, внешнее API и роуты, хуки с состоянием биндингов
  (красный — `required` без биндинга), активити, события с lag/DLQ и
  правилами, workflows с запусками и расписаниями, аудит, телеметрия
  (§15). Где есть схема — формы; где нет — JSON.
- **Плагины** — UI-бандл сервиса (§3.6) как Module Federation remote,
  совпадение `sdk_major` обязательно. Плагин работает с API платформы и
  внутренним API своего сервиса (§3.2) через консольный прокси; клиент —
  сгенерированный `protoc-gen-ws-es` из proto сервиса. Прямого пути
  браузер → сервис нет.

### 11.1 Транспорт — одно ws-proto соединение

Консоль держит одно ws-proto соединение к backplane: `GET /ws`
(`internal/console`). На нём:

- **собственный API backplane** (`backplanepb/console/v1`, пакет
  `backplane.console.v1`), зарегистрированный через `wsrpc.GRPCRegistrar`:
  - `CatalogService` — `ListServices` (сводка: последняя версия, версии,
    инстансы, здоровые, health `HEALTHY`/`DEGRADED`/`DOWN`, есть ли
    внутреннее API и UI), `GetService` (последний манифест, манифесты всех
    версий, инстансы с состоянием и health; нет сервиса — `NOT_FOUND`),
    `WatchCatalog` (сводка сразу и после каждого нового снапшота registry;
    медленный читатель пропускает промежуточные), `ListPlugins` (§11.2);
  - `SessionService` — `ListSessions`, `RevokeSession`,
    `RevokeOtherSessions` (§11.3);
  - `ConfigService` — Live-конфигурация (§5), компонент `internal/config`;
  - `EventService` (§8), `WorkflowService`, `ScheduleService`,
    `CallService` (§9) — компонент `internal/ops`;
- **всё остальное — relay**: `WithUnknownHandler` по полному имени метода
  находит сервис, чей **последний** манифест (`registry.Service.Latest`)
  объявляет этот gRPC-сервис в `internal_services`, выбирает инстанс и
  пробрасывает кадры как есть (`RecvRaw → gRPC → SendRaw`), стриминг
  включительно: gRPC-поток в обе стороны с кодеком «байты как есть».
  Транскодинга нет, backplane содержимое не видит. Заголовки ответа
  сервиса уходят в браузер leading-заголовком, trailers — в END, статус
  ошибки (с details) — как есть.

Выбор инстанса: состояние инстанса есть, фаза `SERVING`, есть адрес и
платформенный порт, и манифест **его** версии объявляет этот сервис
(старые версии, где метода нет, не выбираются). Сначала — зарегистрированные
в каталоге и проходящие health check, среди них случайный; нет таких —
любой подходящий. Соединения к платформенным портам (plaintext gRPC) —
пул по адресу; адреса, которых нет в каталоге, закрываются по изменению
снапшота. Лимит сообщения — 16 MiB в обе стороны.

Правила relay:

1. пробрасываются только методы, объявленные в манифесте как внутреннее
   API; всё прочее — `PERMISSION_DENIED`; объявлен, но нет подходящего
   инстанса — `UNAVAILABLE`;
2. платформенный порт доступен только backplane (сеть); дополнительно
   backplane ставит секрет установки (`BACKPLANE_INTERNAL_SECRET` в своей
   конфигурации) в metadata `bp-internal-secret` (HTTP-заголовок
   `Bp-Internal-Secret` для бандла), SDK проверяет; без него —
   `PERMISSION_DENIED` (HTTP 403);
3. от браузера срезаются `authorization`, `cookie`, все `bp-*` и
   транспортные ключи (`:*`, `grpc-*`, `ws-*`, `content-type`, `te`,
   `host`, `connection`, `user-agent`); backplane ставит
   `bp-console-session` = id сессии соединения. Для собственного API
   backplane то же: `authorization` и `bp-*` срезаются, `bp-console-session`
   ставится, а обработчики берут сессию и из контекста
   (`console.SessionID`);
4. плагины не различаются: один администратор, один origin.

Дедлайн вызова браузера доходит до сервиса; отмена вызова браузером
отменяет вызов к сервису.

В mesh backplane — член mesh и ходит через sidecar; relay не меняется.

### 11.2 Плагины — Module Federation 2.0

Схема та же, что у Grafana и Backstage; реализация — Module Federation
2.0 (рантайм `@module-federation/enhanced/runtime`, Rspack или Vite).

- **Shell — MF-хост.** React, React DOM, роутер и пакеты контекста
  `@gopherex/backplane-*` объявлены совместимыми `shared: singleton`.
  Плагин использует обязательный стек React + TypeScript и общий UI-kit:
  shadcn/Tailwind, недостающие сложные компоненты Grafana через адаптеры,
  семантические токены HyperDX. Подробный контракт —
  [frontend-platform.md](docs/frontend-platform.md).
- **Плагин — MF-remote.** `mf-manifest.json` + чанки; экспонирует
  `./Routes` и `./Nav`. Рядом наш `plugin.json`: `sdk_major`, навигация.
- **Загрузка — динамическая и ленивая.** Shell получает список плагинов
  `CatalogService.ListPlugins`: сервисы, чей последний манифест объявляет
  `ui`, с `hash`, `sdk_major`, путём бандла
  (`<база консоли>/plugins/<service>/<hash>/`) и `available` — есть ли
  живой инстанс с этим бандлом. Для совместимых и доступных shell делает
  `registerRemotes`, страницы грузит `loadRemote` при переходе на
  `/s/<service>/...`. `sdk_major` ≠ shell'у — карточка сообщает, код не
  грузится. Недоступен — плагина нет в навигации, карточка остаётся.
- **Единый UI** = один роутер, один layout (плагин рендерит только
  контент), один UI-kit из shared.
- **Доставка бандла — у сервиса.** Бандл встроен в бинарь (`embed`), SDK
  отдаёт его на платформенном порту по `GET /_backplane/ui/<path>` с
  `ETag` = `ui.hash` и `Cache-Control: no-cache` (§3.6). backplane
  раздаёт его со своего origin: `GET /plugins/<service>/<hash>/<path>`
  (только с сессией). Файл берётся у инстанса `SERVING`, манифест версии
  которого объявляет этот `ui.hash` (здоровые первыми), с
  `Bp-Internal-Secret`; ответ принимается, только если его `ETag` равен
  хэшу (иначе инстанс отдаёт другой бандл — следующий инстанс); `404`
  инстанса — `404`. Путь с хэшем не меняет содержимого, поэтому кэш —
  по `(service, hash, path)` в памяти, LRU с лимитом 64 MiB (файл больше
  четверти лимита отдаётся без кэширования), без перепроверки; одновременные
  промахи одного файла — один запрос к инстансу. Ответ:
  `Cache-Control: public, max-age=31536000, immutable`, `ETag` = хэш;
  `If-None-Match` с ним — `304`. Закэшированный файл отдаётся и без
  живого инстанса. Один origin → нет CORS, cookie работают, браузер
  сервис не видит.

### 11.3 Auth консоли

v0 — один оператор.

- **Admin-токен** — единственная учётка; это конфигурация backplane, а не
  данные: `BACKPLANE_ADMIN_TOKEN` (`config.Secret`, в деплое — секрет),
  обязателен, не короче 16 символов. Нет или короче — конфигурация не
  проходит валидацию, backplane не стартует. backplane токен не генерирует
  и не хранит в PostgreSQL; все реплики берут его из одного секрета.
  `POST /auth/login` сравнивает SHA-256 присланного и настроенного токена
  за постоянное время (`crypto/subtle`), поэтому не утекают ни содержимое,
  ни длина.
- **Смена токена** — оператор меняет секрет в деплое и перекатывает
  реплики. Сессии в PostgreSQL от смены не зависят: открытые старым
  токеном живут до своего истечения или отзыва — `RevokeSession` по одной
  либо `SessionService.RevokeOtherSessions`, который удаляет все сессии,
  кроме текущей, и закрывает их соединения.
- **Вход** — `POST /auth/login` с `{"token": "..."}` → `200` с
  `{id, created_at, expires_at, idle_timeout_seconds}` и cookie
  `bp_session` (`HttpOnly; Secure; SameSite=Strict; Path=<база консоли>`).
  В cookie — случайный токен сессии (32 байта); в PostgreSQL
  (`backplane.console_session`) — только его SHA-256, плюс публичный id,
  создана, истекает, last seen, адрес, user-agent. Абсолютный срок 12 ч,
  idle 1 ч: активность — запрос с cookie или RPC на `/ws` (пишется не чаще
  раза в минуту). `POST /auth/logout` удаляет сессию и закрывает её
  соединения; `GET /auth/session` — текущая сессия или `401`.
  `SessionService.ListSessions` — живые сессии (текущая помечена),
  `RevokeSession` — удалить сессию и закрыть её соединения. Истёкшие
  сессии удаляются раз в 10 минут.
- **`/ws` upgrade** — только с живой сессией и только если `Origin` входит
  в `BACKPLANE_CONSOLE_ORIGINS` (точное `scheme://host[:port]`), а без
  списка — если host `Origin` равен `Host` запроса либо, когда в `Host`
  нет порта, `Host` с портом из `X-Forwarded-Port` (reverse proxy
  отбрасывает порт из `Host`, §6: так консоль на нестандартном порту,
  `localhost:8081`, узнаёт свой origin). Без `Origin` —
  отказ (`403`). Соединение живёт, пока жива сессия: отзыв на этой
  реплике закрывает его сразу, на другой — перечитывание сессии раз в
  30 с.
- **CSRF** — cookie `SameSite=Strict`; `/ws` проверяет `Origin`;
  `POST /auth/login` и `/auth/logout` отвергают чужой `Origin`, а без
  `Origin` — `Sec-Fetch-Site`, отличный от `same-origin`/`none`
  (клиент не из браузера не шлёт ни того, ни другого).
- **Brute force** — счётчики в Valkey, общие для всех реплик: на адрес —
  5 попыток в минуту (окно с первой попытки); глобально: после 10
  неудачных подряд (с любых адресов, без перерыва дольше часа) каждый
  вход ждёт 1 с, удваиваясь с каждой следующей неудачей до 1 мин;
  успешный вход сбрасывает. Отказ — `429` с `Retry-After`. Недоступный
  Valkey закрывает вход (`503`): попытки, которые нельзя сосчитать, не
  пропускаются. Адрес клиента — адрес пира,
  а если пир входит в `BACKPLANE_CONSOLE_TRUSTED_PROXIES` (reverse proxy) — первый
  справа в `X-Forwarded-For`, не входящий в список.
- **Заголовки** всех ответов: CSP `default-src 'self'; script-src 'self'`
  (без inline-скриптов; `style-src` допускает inline-стили UI-kit),
  `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`,
  COOP/CORP `same-origin`, `Strict-Transport-Security`. Ответы `/auth/*` —
  `Cache-Control: no-store`.
- **Разработка по HTTP** — `BACKPLANE_CONSOLE_INSECURE_COOKIE=true`
  убирает `Secure` у cookie и HSTS.
- **Позже** — вход через identity-сервис по OIDC; сессия остаётся
  абстракцией (`console.Sessions`). Не в v0.

Компонент console слушает `BACKPLANE_CONSOLE_LISTEN` (`:8081`) и обслуживает shell,
`/auth/*`, `/ws`, `/plugins/*`, `/schemas/*` под `BACKPLANE_CONSOLE_PREFIX` или `/`.
Cookie использует этот path. Прокси установки при наличии сохраняет путь и поддерживает
WebSocket. Console host определяется деплоем; такого поля в backplane нет.

### 11.4 Пакеты UI и разработка модулей

Пакеты публикуются только в GitHub Packages как `@gopherex/backplane-*`,
одной согласованной версией; её major — `sdk_major`. API, транспорт,
React-адаптеры, тема, компоненты и контракт плагина разделены по
[спецификации](docs/frontend-platform.md#package-boundaries).

Сгенерированные клиенты покрывают всё `backplane.console.v1` и внутреннее
API модуля. `useClient` использует одно соединение хоста с cookie-сессией;
временные сетевые ошибки не разлогинивают, мутации с неизвестным исходом
автоматически не повторяются. `Watch*` восстанавливается по контракту
конкретного метода.

Обе темы — dark (по умолчанию) и light — с начала разработки. i18next с
English-only словарями и пространством ключей каждого модуля. Отдельного
SDK для внешнего пользовательского UI нет. Полнота кита и происхождение
компонентов — [матрица](docs/ui-components.md).

Автор запускает UI модуля через `yarn dev` или готовый контейнер шаблона,
без backplane и его инфраструктуры. Standalone entry предоставляет тот же
контекст, который embedded entry получает от платформы; локальный API,
фикстуры и окружение выбирает автор. Число страниц и их содержимое — у
модуля; относительные маршруты под `/s/<service>/` и реестр навигации — по
контракту платформы. В проде бандл по-прежнему сервит модуль (§11.2).
`make dev` платформы — отдельная среда для разработки консоли и проверки
интеграции, с backplane, hello, formatter, биндингом и правилом.

## 12. Хранилище и конфигурация backplane [backplane]

### 12.1 Хранилище

Consul KV персистентен (Raft, снапшоты), но это не БД: нет истории,
запросов кроме get/list, транзакций сверх `txn` на 64 операции, значение
≤ 512 KB. Temporal и так требует PostgreSQL — backplane использует **тот же
инстанс, свою схему `backplane`**.

| где | что |
|---|---|
| **PostgreSQL**, схема `backplane` | версии биндингов и правил (§7.1, §8.1); ревизии Live-значений; сессии консоли; аудит |
| **Consul KV** | манифесты и состояние инстансов — proto binary (пишет SDK); `config/<service>/` — доставка Live-значений: скаляр — текстом, контейнер — JSON, плюс `_revision` (пишет backplane, §5.3) |
| **Temporal** | запуски хуков, биндингов, правил с историей — не дублируются |

backplane горизонтально масштабируется: всё состояние — в PostgreSQL,
Consul и Temporal; реплики равноправны, singleton'ов нет. Reconciler на
нескольких репликах пишет одно и то же — идемпотентно; consumers правил
разделяются NATS; Nexus- и binding-worker'ы — обычные Temporal-воркеры;
Деплой определяет балансировку между репликами.

### 12.2 Конфигурация backplane

backplane — сервис на своём SDK (§14): весь блок §4.3 плюс своё:

| | |
|---|---|
| `BACKPLANE_PG_DSN` | обязателен; схема `backplane` — создаётся и мигрируется при старте |
| `BACKPLANE_PG_QUERY_LOG`, `_POOL_MAX_CONNS`, `_POOL_MIN_CONNS`, `_POOL_MAX_CONN_LIFETIME`, `_POOL_MAX_CONN_IDLE_TIME` | нижний уровень лога запросов (`warn`; аргументы не пишутся) и границы пула (`0` — по умолчанию pgx; в проде `MAX_CONNS` задают явно) |
| `BACKPLANE_CONSUL_ADDR` | обязателен для backplane (у сервисов — опционален) |
| `BACKPLANE_VALKEY_ADDR`, `_USERNAME`, `_PASSWORD`, `_DB`, `_TLS_*` | обязателен адрес (`host:port`): Valkey для попыток входа (§11.3); старт ждёт его, readiness — его PING |
| `BACKPLANE_CONSOLE_LISTEN` | HTTP консоли (`/`, `/ws`, `/auth`, `/plugins`) — на собственном listener (`:8081`) |
| `BACKPLANE_ADMIN_TOKEN` | обязателен: admin-токен консоли (секрет деплоя, не короче 16 символов; §11.3) |
| `BACKPLANE_CONSOLE_PREFIX`, `_ORIGINS`, `_TRUSTED_PROXIES` | console path и browser/proxy настройки; списки — JSON |
| `BACKPLANE_CONSOLE_INSECURE_COOKIE` | `true` — cookie сессии без `Secure` и без HSTS: только разработка по HTTP (`false`) |
| `BACKPLANE_OBS_METRICS_URL`, `_LOGS_URL`, `_TRACES_URL` | observability (опционально) |
| `BACKPLANE_AUDIT_LISTEN`, `_KEYS` | OTLP/gRPC-listener аудита приложений для Collector (`:4317`), только во внутренней сети; ключи — JSON-массив (§14) |
| `BACKPLANE_AUDIT_RETENTION` | срок хранения аудита управления (`0s` — бессрочно); аудит приложений не истекает |
| `BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL` | период прохода reconciler'а `config/` сверх прохода на старте и по изменению KV (`30s`, больше 0; §5.3) |

## 13. Безопасность (v0)

Модель доверия: одна установка, доверенная внутренняя сеть, один
оператор. Ничего своего — у каждого компонента родной auth, креды выдаёт
деплой в конфигурации.

1. **Границы.** Внешний периметр принадлежит деплою; TLS завершается до приложения
   (LB, ingress) — решает деплой. Внутренние порты наружу не публикуются.
   Внутри plaintext по умолчанию; каждый клиент SDK — Consul, NATS,
   Temporal — умеет TLS через конфигурацию (`config.TLS`: PEM-содержимое из
   секретов, не пути к файлам; клиентский сертификат — парой).
2. **Consul ACL.** Токен на сервис: регистрировать себя, читать каталог,
   писать свои манифесты и состояние, читать все манифесты (для «подписан
   на всё»), читать свой `config/<name>/`. Токен
   backplane: читать каталог и `backplane/*`, писать `config/*`. Шаблонов
   политик в репозитории нет — их пишет деплой. В dev без ACL работает.
3. **NATS.** Пользователь на сервис: publish `bp.<name>.>` и
   `bp.dlq.<name>.>`, subscribe `bp.>`, JetStream API на свои стримы
   (`bp_<name>`, `bp_dlq_<name>`), на создание отсутствующих `bp_*`
   (подписчик создаёт стрим источника) и на свои consumers `<name>__*`.
   backplane — читать всё, JS API для consumers правил. В dev без auth
   работает.
4. **Temporal.** Один namespace; per-service авторизация в OSS требует
   authorizer-плагина — в v0 нет, доверие внутри namespace явное. Nexus
   endpoint access policy — allowlist namespace. Temporal Cloud — API key
   (`BACKPLANE_TEMPORAL_API_KEY`, §4.3) поверх TLS.
5. **backplane → платформенный порт сервисов.** Сеть плюс
   `BACKPLANE_INTERNAL_SECRET` в конфигурации обеих сторон. Пустой секрет
   выключает проверку: внутреннее API и бандл открыты всем, кто достал до
   порта, — `Run` предупреждает об этом в лог. Пробы и `grpc.health.v1`
   секретом не закрыты. SDK сравнивает секрет за постоянное время
   (HTTP и gRPC одинаково).

   **Ротация секрета** без простоя: (1) сервисам — новый секрет в
   `BACKPLANE_INTERNAL_SECRET`, старый — в `BACKPLANE_INTERNAL_SECRET_PREVIOUS`
   (rollout; оба принимаются); (2) backplane переходит на новый;
   (3) сервисам убрать `_PREVIOUS` (rollout). `_PREVIOUS` без основного
   секрета проверку не включает.
6. **Секреты.** Поле типа `config.Secret` маскируется везде, где его
   выводит SDK (fmt, лог, JSON, эффективная конфигурация в состоянии
   инстанса, схема — пометка `secret`). **[backplane]** В карточке и
   истории — по пометке `secret` в схеме, без схемы — предупреждение и
   показ как есть. Live-значения лежат в PostgreSQL и KV открытым текстом
   под защитой доступа; шифрование at rest — не в v0.
7. **[backplane] Консоль** — §11.3: admin-токен — секрет деплоя
   (`BACKPLANE_ADMIN_TOKEN`), в базе не хранится; сессии — только SHA-256
   их токенов; CSP `script-src 'self'`, без inline.
8. **[backplane] Хуки** — авторизация = биндинг: вызывается только
   привязанное.

Не в v0: несколько пользователей, RBAC, mesh/mTLS между сервисами,
per-service auth в Temporal, шифрование at rest.

## 14. Аудит [backplane]

Сторона SDK — memo `source` и search attributes запуска хука (§7.2);
остальное — сервер.

Принцип: не дублировать то, что стек уже пишет.

| что | где | что видно |
|---|---|---|
| запуск хука, биндинга, правила | Temporal history | вход, выход, шаги, ошибки, время; кто — в memo (`source: iam/<instance>` у `Call` вне workflow, `console: <session>` у запуска из консоли); поиск — по search attributes `BpService`, `BpHook`, если namespace их завёл (Keyword; без них SDK вызывает без атрибутов) |
| история Live-значений | ревизии в PostgreSQL | значения, автор, комментарий, время, откат |
| обращения к внешнему API | access-логи приложения и внешнего прокси | метод, статус, латентность, клиент |
| события | сам JetStream-стрим | replayable лог с `ce-source`, `ce-time` |

Реализован [долговечный аудит M3](docs/audit-api.md): таблицы
`audit_entry`, `audit_outbox` и `audit_clock` в PostgreSQL. Записываются все
управляющие действия оператора, включая конфиг, биндинги, правила,
запуски, команды workflows/расписаний, redrive/purge и сессии. Чтения и
Watch не аудируются. Лог доступа к непрозрачному relay — отдельный поток,
без тел запросов и ответов.

Изменение PostgreSQL, запись аудита и outbox (при включённом экспорте) фиксируются в одной
транзакции. Команда во внешний стек имеет запись намерения и отдельный
результат с общим operation ID; неизвестный исход после сбоя остаётся
неизвестным. Outbox экспортирует OTel Logs через OTLP с устойчивым ID,
как минимум один раз. Экспорт опционален и не зависит от NATS; история
всегда остаётся в PostgreSQL и доступна через AuditService. В записи допускаются только
идентификаторы и перечисленные в контракте метаданные; payload, значения
конфига, комментарии ревизий и произвольные тексты ошибок не копируются.
Слова оператора у команд запусков и расписаний сохраняются, обрезанными до
256 символов: имя сигнала (не аргумент), причина terminate, note паузы и
возобновления.

`AuditService` читает одну ленту — записи платформы и приложений вместе
(представление `audit_feed`, detail записи платформы — её атрибуты):
`SearchAudit` (страницы от новых к старым), `AuditHistogram`, `AuditFields`,
`AuditFacets`; фильтр — интервал, текст и условия на поля и атрибуты
(is / is not / contains / starts with / exists / сравнения чисел). Запросы —
типизированные динамические запросы sqld. Retention — только конфигурация
деплоя, в UI нет ни настройки, ни отображения срока хранения.

**Аудит приложений.** Приложение аудирует свои действия обычной
OTel-записью лога с атрибутом `backplane.audit=true`; действие —
`event.name`, остальное по соглашению — `backplane.audit.actor`,
`.subject`, `.outcome`, `.id` (дедупликация). SDK: `backplane.Audit()` и
константы ключей. Лог идёт, куда идут логи; pipeline аудита в Collector
деплоя (`deployments/otel-collector.audit.yaml`: filter по метке, очередь
на диске) пересылает помеченные записи по OTLP/gRPC `LogsService` на
внутренний listener backplane (`BACKPLANE_AUDIT_LISTEN`, `:4317`;
`BACKPLANE_AUDIT_KEYS` — ключи). Backplane хранит их в PostgreSQL без
срока (`app_audit`, дедупликация повторов Collector'а, ответ после
commit) и отдаёт их в общей ленте аудита. Сторонний сервис (identity-сервер и т.п.)
аудируется меткой, которую ставит Collector, — код сервиса не меняется.
Записи, пришедшие через OTLP-прокси backplane (браузеры), Collector
отбрасывает по заголовку `X-Backplane-Ingest: proxy`. Контракт —
[docs/audit-api.md](docs/audit-api.md#application-audit). Консоль: раздел
Audit — одна таблица с фильтрами, гистограммой, полями и живым обновлением.

## 15. Observability

Весь OTel-стек самостоятельный: Collector, обработка, экспортёры,
хранилища и retention принадлежат деплою и работают без backplane.
Backplane предоставляет OTLP-прокси по модели komeet-gw и авторизованный
API чтения через драйверы хранилищ. Приложения могут
отправлять сигналы прямо в Collector. Сам backplane — обычный отправитель
телеметрии; он не управляет жизненным циклом OTel-стека.

Прокси ограничивает транспортную нагрузку и передаёт содержимое OTLP
без изменения, помечая запрос заголовком `X-Backplane-Ingest: proxy`
(так Collector не принимает через прокси аудит приложений, §14). Декодирование сигналов, редактирование чувствительных
данных, обогащение и агрегация выполняются внешним OTel-стеком.
Собственного формата хранения, копий записей и архива метрик у backplane нет.
Подробный [контракт M3](docs/frontend-platform.md#platform-telemetry)
задаёт приём, аутентификацию, точность данных и языки запросов.

**Ошибки** — часть платформы. Ошибка — это
OTel-запись лога в хранилище логов деплоя: браузерный SDK
`@gopherex/backplane-errors` (любой фронтенд, через публичный OTLP-приём)
пишет исключение с состоянием приложения и историей в теле записи;
сервисы — `backplane.CaptureError` и перехваченные паники по конвенциям
`exception.*`; годится и любая чужая запись с этими полями. Хранится
столько, сколько хранилище держит логи; копий, удаления и группировки в
задачи нет. `ErrorService` читает их через драйвер хранилища (LogsQL
строит `internal/obs/logsql`), консоль — раздел Errors (лента, стек,
состояние, история, трейс, связанные логи). Контракт —
[docs/errors.md](docs/errors.md).

Установка — одна граница доверия/тенант; приложения и окружения —
атрибуты, изолированные тенанты используют отдельные установки.

### 15.1 Сквозной контекст

Все части стека умеют OpenTelemetry; SDK и backplane пропагируют его:

- Внешний прокси при поддержке OTel — span на входящий запрос, `traceparent` в сервис;
- SDK — OTel из коробки (xtrace): серверы инструментированы, логи с
  `trace_id`, экспорт по стандартным `OTEL_*`; каждый узел дерева — свой
  трейсер и метр (`<service>/<путь>`);
- Temporal — OTel-интерцепторы: workflow, activity, Nexus-операция — хук
  из IAM → биндинг → activity в smtp = один trace;
- NATS — `traceparent` в CloudEvents;
- backplane — сам сервис: relay, reconciler, правила — span'ы.

**Resource-атрибуты ставит SDK**: `service.name`, `service.instance.id`,
`service.version`, `deployment.environment.name` (если задан
`BACKPLANE_ENVIRONMENT`). Автор ничего не пишет.

### 15.4 Что пишет SDK

Метрики SDK — метр `github.com/gopherex/backplane`:

| метрика | тип | атрибуты | когда |
|---|---|---|---|
| `backplane.dependency.provide.attempts` | counter | `node`, `outcome` (`ok`, `error`) | каждая попытка `Provide` обязательной или опциональной зависимости |
| `backplane.dependency.ready` | gauge 0/1 | `node` | значение предоставлено (и проходит пробу при `ProbeOptional`) |
| `backplane.config.updates` | counter | `outcome` (`applied`, `rejected`) | обновление конфигурации из KV (§5.5) |
| `backplane.config.degraded` | gauge 0/1 | — | слой Consul пуст или устарел |
| `backplane.hook.call.duration` | histogram, s | `hook`, `outcome` (`ok`, `no_binding`, `unavailable`, `timeout`, `error`) | каждый `Call` хука |
| `backplane.event.published` | counter | `event`, `outcome` (`ok`, `unavailable`, `timeout`, `error`) | каждый `Publish` |
| `backplane.reactor.duration` | histogram, s | `consumer`, `outcome` (`ack`, `nak`, `dead`, `terminal`) | каждая доставка реактору и каждый dead letter в `Redrive` |
| `backplane.activity.duration` | histogram, s | `activity` (`<service>.<Name>`), `outcome` (`ok`, `error`) | каждое исполнение `activity.Handle` |
| `backplane.transport.connected` | gauge 0/1 | `transport` (`consul`, `nats`, `temporal`) | при каждом изменении соединения |
| `backplane.panics` | counter | `where` (`grpc.public`, `grpc.internal`, `wsproto`, `http.public`, `http.platform`, `reactor`, `activity <service>.<Name>`) | каждая перехваченная паника |

Кроме них: метрики хоста и Go-рантайма (xtrace), метрики Temporal SDK
(метр `github.com/gopherex/backplane/temporal`), otelgrpc и otelhttp
серверов.

Span'ы SDK:

- gRPC-серверы обоих классов портов — otelgrpc (кроме `grpc.health.v1`);
  HTTP публичных портов — otelhttp (операция `public<addr>`); ws-proto —
  server span на вызов (имя — полный метод, `rpc.system` = `ws-proto`).
- `publish <service>.<Event>` (producer) на каждый `Publish`; контекст
  уходит в `traceparent` события. `process <service>.<Event>` (consumer)
  на каждую доставку реактору, в trace события; атрибуты `messaging.*`
  (система `nats`, subject, durable, номер доставки).
- Temporal — интерцепторы OTel SDK Temporal: запуск `CallHook`, workflow,
  Nexus-операция, activity. `activity <service>.<Name>` (consumer) — когда
  заголовки Temporal не донесли trace, активити продолжает trace из
  `ActivityCall.trace` со ссылкой на текущий span.
- Узлы дерева — `scope.Span` / `deps.SpanValue` на трейсере
  `<service>/<путь>`.

### 15.2 Источники, карточка и Explore [backplane]

Объявление телеметрии модулем необязательно. Источники находятся по
сохранённым сигналам, включая инфраструктуру и внешние приложения.
Группировка — `service.namespace → service.name → service.instance.id`,
исходные имена и инстансы сохраняются. Обёртка и Kratos могут иметь общий
namespace, но разные service.name. Атрибуты задаются стандартными OTel
средствами или обогащаются Collector, только когда источник известен.

Необязательные селекторы связывают карточку модуля с несколькими
источниками. Приоритет: настройка деплоя → декларация модуля →
сопоставление по OTel-конвенциям. Они задают начальный фильтр карточки,
не ограничивают приём, хранение или глобальный Explore. Не связанные с
модулем источники видны в Explore. Общий namespace не создаёт trace:
для причинной связи нужен настоящий trace context.

Карточка показывает метрики, логи и трейсы с переходами по trace ID и
контексту источника. Generic-компоненты принимают данные через props;
API-aware композиции — отдельный пакет. Retention в UI не показывается.

### 15.3 Драйверы и деплой [backplane]

| драйвер | метрики | логи | трейсы |
|---|---|---|---|
| **victoria** (M3) | VictoriaMetrics, MetricsQL / совместимый PromQL | VictoriaLogs, LogsQL | VictoriaTraces, TraceQL по проверенным capabilities; LogsQL для сырых span; полный trace по ID |
| prometheus/loki/tempo (позже) | PromQL | LogQL | TraceQL |

API предоставляет типизированные фильтры, агрегации и discovery, а также
сырой запрос с явными signal, language, expression, границами времени и
лимитами. Экспериментальная поддержка TraceQL в VictoriaTraces не означает
полную совместимость с Tempo: версия и используемый поднабор проверяются.
Все поля, тела и timestamps, доступные в API хранилища, доступны модулям,
не только встроенным карточкам. Драйвер сохраняет возвращённые значения
и точность, явно описывает отображение полей и ограничения backend.
Восстановление исходных типов из строковых полей хранилища не обещается.

UI читает через cookie-сессию, сервер модуля — через защищённый API с
существующим `BACKPLANE_INTERNAL_SECRET`. OTLP внешних приложений принимается
без обязательного ключа и пользовательской сессии; требование ingest-only
ключа опционально задаётся деплоем. API выпуска ключей нет. Приём не зависит
от IAM и приложения. Данные публичного отправителя недоверенные; браузерный
ключ не удостоверяет личность. Потоковый лимит тела, IP rate/burst, общий
предел одновременных запросов на реплику, ограниченная память и timeout
Collector защищают приёмник. Ограничен и размер таблицы IP-лимитеров;
перегрузка возвращает явный отказ с Retry-After. Общий бюджет нескольких
реплик обеспечивает инфраструктура деплоя. Настройки Collector, хранилищ, лимитов, retention,
скрейпинга внешнего прокси/Temporal/NATS принадлежат деплою. Нет данных — явное
пустое состояние, недоступен backend — ошибка, не ложная пустота.

Не в M3: alerting, редактор дашбордов, панели как данные, собственный
движок хранения телеметрии.

## 16. Границы v0, milestones, риски

### 16.1 Не-цели v0

Деплой и оркестрация; service mesh; несколько пользователей и RBAC;
alerting; редактор дашбордов; шифрование at rest; per-service auth в
Temporal; Kafka-драйвер; gateway через Consul config entries; вход через
identity-сервис; CaC/CLI/GitOps-канал доставки конфига; партиции событий;
`template upgrade`.

### 16.2 Milestones

Статус: M0 сделан; M1 и M2 сделаны на стороне сервера, кроме фронтенда
консоли; M3 — доставка бандла и начальная frontend-основа (`web/`),
полная приёмка не завершена.

**M0 — SDK и `hello`, без backplane. Сделано.** Platform-in-a-box:
compose с Consul, NATS, Temporal (dev-сервер с Nexus), PostgreSQL и Valkey. SDK: конфигурация из env/файла с
Live-полями из KV и `Validate`, дерево узлов с зависимостями и бюджетом
остановки, манифест и состояние инстанса в KV, регистрация в Consul,
платформенный порт (cmux: внутреннее API за секретом и гейтом, health,
xprobe, бандл, info, pprof), managed- и declarative-роуты с политикой
документация API, OTel (resource, серверы, логи с trace_id), `backplanetest`.
`hello` объявляет все разделы манифеста. Доказывает: сервис живёт без
backplane; манифест и слои конфига верны.

**M1 — backplane: конфиг, gateway, консоль. Сервер сделан, фронтенда
консоли нет.** Есть (`cmd/backplane`, §12.2): registry — blocking queries
к каталогу Consul, health и `backplane/services/`, снапшот `Catalog` после
каждого изменения (`internal/registry`); схема PostgreSQL со своими
миграциями (`internal/store`); Live-конфигурация — ревизии в PostgreSQL,
валидация override схемой на каждом живом инстансе, доставка в
`config/<service>/` с `_revision`, откат, reconciler (§5.2, §5.3,
`internal/config`); бэкенд консоли — вход по admin-токену и сессии,
`/ws` с `CatalogService`, `SessionService`, `ConfigService` и relay
ws-proto → внутреннее API, `/plugins/` с кэшем по хэшу (§11,
`internal/console`). Нет: shell с карточками (инстансы, конфигурация по
инстансам с историей Live-значений, внешнее API) — `GET /` консоли
отдаёт заглушку. Доказательство — `conformance/m1_test.go` (`make
test-m1`, §16.3): цикл «форма → ревизия → KV → сервис» и вход через console listener
на настоящих бинарях `backplane` и `hello`.

**M2 — хуки, биндинги, правила, события. Сервер сделан, фронтенда
консоли нет.** Сторона SDK: Temporal в SDK — хуки (`Call` через
`backplane.CallHook.v1` на очереди `<service>.hooks`, `CallHook` —
Nexus напрямую, ключ идемпотентности), активити обоих видов, workflows
(`Register`, `Declare`, `Client`), настройка
worker'а; события CloudEvents над JetStream, реакторы с опциями доставки,
DLQ и `Redrive`. Сторона backplane: биндинги и правила — версии в
PostgreSQL, валидация по последним манифестам с CEL над JSON-схемами,
определения как YAML/protojson (шаги по имени, значения — деревья CEL),
`BindingService`, `RuleService` и `WiringService` на `/ws` (§7.1, §8.1,
`internal/bindings`); исполнитель — Nexus endpoint на каждый сервис с
хуками, Nexus-only worker `<service>.Hooks`, workflow
`backplane.Binding.v1` с шагами и компенсациями, запуски в консоли
(`TestBinding`, `ListBindingRuns`, `GetBindingRun`, `CancelBindingRun`;
§7.2, `internal/executor`); движок правил — consumer
`backplane__rule-<id>` на правило, запуск `rule/<id>/<ce-id>` на
событие, пауза и возобновление, `TestRule` и запуски правила
(`internal/rules`); операции консоли — события, стримы, lag, тестовые
события, dead letters с redrive реактору и purge, workflows, запуски,
расписания, вызов хука и активити из консоли (§8, §9, `internal/ops`,
контракт имён `internal/wire`). `hello` объявляет хук `Greet`
(вызывается из HTTP-handler'а и из workflow), активити `Echo` и событие
`Greeted`; биндинг `hello.Greet` → `hello.Echo` и правило на
`hello.Greeted` → `hello.Echo` сохраняются в консоли (раздел Wiring: YAML и
граф). Доказательство —
`conformance/m2_test.go` (`make test-m2`, §16.3): развязка end-to-end и
один trace через всё на настоящих бинарях `backplane` и `hello`.

**M3 — плагины, observability, аудит.** Сделана доставка бандла:
отдача с платформенного порта в SDK и `/plugins/<service>/<hash>/` консоли
с кэшем по хэшу (§11.2). Целевой объём: пакеты API/клиента/кита, MF-хост, standalone-шаблон и
плагин `hello`; OTLP и Collector, драйвер `victoria`, аудит с outbox и
опциональным OTLP Logs экспортом. Этапы приёмки —
[frontend-platform.md](docs/frontend-platform.md#implementation-and-acceptance-order).

**Дальше, вне репозитория** — Kratos-wrapper, `template`, `smtp` (courier
режется на атомарные способности), биндинг `iam.SendEmail`. Любой special
case в SDK ради них — дефект модели.

### 16.3 Эталонные сервисы, conformance, шаблон

- Эталонная связка — `examples/hello` и `examples/formatter`: независимые
  процессы, хук `hello.Greet` вызывает активити formatter, а `Greeted`
  обрабатывается правилом и реактором. Событие публикуется и при связанном
  хуке. `examples/demo/cmd/setup` сохраняет биндинг и правило через API консоли.
  Двухрепличный сценарий — `make test-replicas` (§19).
- **Conformance** (`conformance/`, против `make up`; запуск —
  `deployments/README.md`) —
  тесты над контрактом SDK снаружи: по сети, Consul, NATS и Temporal.
  Каждый набор включается своей переменной и без неё пропускается:
  `BACKPLANE_TEST_CONSUL`, `BACKPLANE_TEST_NATS`,
  `BACKPLANE_TEST_TEMPORAL`. Сторону backplane (Nexus endpoint, handler,
  binding-workflow) там играет сам тест. Проверяется:
  - манифест и состояние инстанса в KV, регистрация и check в каталоге,
    уход из каталога и KV на остановке;
  - платформенный порт: health открыт, внутреннее API и бандл за
    секретом; бандл с `ETag` = `ui.hash`, `Cache-Control: no-cache` и
    `304` на `If-None-Match`;
  - внешнее API: gRPC, стрим, HTTP, ws-proto;
  - слои конфига и hot reload по записи в KV (источник KV в состоянии);
  - хук из HTTP-handler'а и из `OnStart`/`OnStop` через биндинг, который
    вызывает активити по имени с JSON-входом; `hook.Key` исполняет
    биндинг один раз; `no binding`;
  - хук из workflow (`CallHook`); активити на workflow по имени с
    envelope `ActivityCall`;
  - событие с корректными `ce-*` и trace до реактора;
  - реакторы через `backplane.Open`: DLQ по терминальной ошибке и после
    последней доставки, заголовки dead letter, `Redrive`; обработчик,
    прерванный остановкой, возвращает сообщение без DLQ; эволюция схемы
    (лишнее поле — игнор, JSON и protojson; отсутствующее — нулевое);
  - **[backplane]** redrive из консоли (`TestConsoleRedrive`): сервис на
    SDK с двумя реакторами на своё событие, тестовые события консоли
    доходят до обоих, dead letter одного реактора `RedriveDeadLetters`
    возвращает только ему (тот же `ce-id`, `Attempt` 1), второй его не
    видит, dead letter удалён;
  - workflows и административные расписания: клиент, выполнение задания,
    сохранение определения и операторской паузы при перезапуске реплики.

  Другой язык SDK проходит этот же набор.
- **M1 end-to-end** (`make test-m1`): настоящие backplane и hello на собственных
  динамически выделенных портах, scratch PostgreSQL и Consul. Проверяет login/cookie,
  WebSocket, каталог, config validation/save/apply/rollback, reconciler, внутренний
  relay, immutable UI bundle и уход/возврат hello с актуальной ревизией.
- **M2 end-to-end** (`make test-m2`): те же прямые listener'ы плюс NATS и Temporal.
  Проверяет fallback, binding и rule, CEL validation, runs, один trace через HTTP/hook/
  workflow/activity, deduplication, pause/resume и test execution.
- **Две реплики** (`make test-replicas`): backplane ×2, hello и formatter; общие
  config/reconciliation, cross-service binding, dedup, crash/rejoin, Nexus retirement
  и аудит. Внешний gateway не требуется. Suites владеют именами эталонных сервисов
  и очищают только созданные ими ресурсы.
- **Шаблон сервиса** — один (не реализован); генерирует `proto/`,
  `internal/`, `cmd/`, `ui/`, Makefile, Dockerfile, conformance-таргет;
  версия шаблона записывается; `template upgrade` — v1.

### 16.4 Известные риски

- **[backplane] Регистрация Nexus-операций.** В Temporal Go (SDK 1.49,
  nexus-rpc 0.7) набор Nexus-сервисов и операций фиксируется до старта
  worker'а, диспетчеризация — по точному имени, catch-all нет. Исполнитель
  держит отдельный Nexus-only worker и при изменении набора хуков
  стартует новый, затем останавливает старый — задачи в полёте
  дорабатывают на старом, основной worker не трогается (§7.2). Остаточный
  риск — `NOT_FOUND` от реплики, ещё не увидевшей новый хук, в окне
  распространения изменения реестра.
- **Активити по имени.** Единственное место, где backplane формирует
  данные для чужого кода: envelope `ActivityCall` наш, payload — JSON из
  CEL. Сторону SDK conformance проверяет, играя backplane (§16.3).
- **[backplane] Гибридный UI-kit.** Сложные Grafana-компоненты и
  заимствования HyperDX требуют проверки тем, overlays, singleton-контекста,
  CSP и размера сборки. Сначала технический стенд, затем каталог и shell.

## 17. Конвенции имён

| что | как |
|---|---|
| имя сервиса | `^[a-z][a-z0-9-]*$` (иначе `backplane.ErrConfig` из `Open`), уникально в установке; `dlq` зарезервировано (узел `nats` его отвергает) |
| id инстанса | `<service>-<hostname>` (или `BACKPLANE_INSTANCE`, опция `backplane.Instance`) = Consul service ID = `service.instance.id` = identity Temporal-клиента |
| Consul: регистрация | имя — сервис; адрес `BACKPLANE_ADVERTISE` / `POD_IP` / hostname; `Port` — основной публичный порт (0 без внешнего API); теги `BACKPLANE_CONSUL_TAGS`; gRPC check на платформенный порт; `Meta` не заполняется |
| Consul: сессия | `<instance>#<инкарнация>`, `LockDelay` 1 мс, TTL `session_ttl` |
| Consul KV | `backplane/services/<service>/manifests/<version>` (dev-версия `0.0.0` — `0.0.0+<12 hex хэша манифеста>`), `backplane/services/<service>/instances/<id>`, `config/<service>/<path>` — только пути Live-полей (вложенность — `/`; скаляр — строкой, контейнер — JSON); ревизия — `config/<service>/_revision` (не Live-путь: SDK не применяет его как значение) |
| env | `<SERVICE>_<PATH>`: `SERVICE` — имя сервиса в верхнем регистре, `-` и `.` → `_`; путь — JSON-имена полей в верхнем регистре через `_`; блок SDK — `BACKPLANE_<PATH>` (таблица §4.3) |
| файл | `BACKPLANE_CONFIG_FILE` (YAML/JSON) той же формы, что структура конфигурации |
| платформенный порт | `/healthz/liveness`, `/healthz/readiness`, `/healthz/startup`, `/_backplane/ui/<path>`, `/_backplane/info`, `/debug/pprof/*`; секрет — gRPC metadata `bp-internal-secret`, HTTP `Bp-Internal-Secret` |
| NATS | стрим `bp_<service>`, subject `bp.<service>.<Event>` (фильтр стрима `bp.<service>.>`); durable consumer реактора `<subscriber>__<consumer>` (`consumer` — `subscriptions[].consumer` манифеста: `<путь узла>:<событие>`, на `Root` — `<событие>`, или `event.Consumer`); DLQ — subject `bp.dlq.<subscriber>.<consumer>`, стрим `bp_dlq_<subscriber>`; redrive из консоли — subject `bp.<src>._redrive.<subscriber>.<consumer>` в стриме источника (второй фильтр consumer'а реактора), заголовок `bp-redriven`, `Nats-Msg-Id` `redrive:<durable>:<seq>`; тестовое событие консоли — `ce-instance` `backplane`, `ce-bptest` `true`, `ce-bpauthor`; имена экранируются (§8); metadata стримов `bp.service`, `bp.kind` (`events` / `dead-letters`), `bp.ensured-by`, consumer'ов `bp.service`, `bp.consumer`, `bp.event`; заголовки dead letter `bp-error`, `bp-consumer`, `bp-delivered`; **[backplane]** consumers правил `backplane__rule-<id>` (metadata `bp.rule`, `bp.rule-version`) |
| Temporal | namespace один (`BACKPLANE_TEMPORAL_NS`); task queue сервиса `<service>`; очередь вызовов хуков `<service>.hooks`; Nexus endpoint `<service>`, Nexus service `<service>.Hooks`, операция `<Name>`; activity type — имя активити; workflow type активити на workflow и `workflows.Declare` — объявленное имя; workflow вызова хука вне workflow `backplane.CallHook.v1`, id `hook/<service>/<Name>/<uuid>` или `hook/<service>/<Name>/<key>`, memo `source` = `<service>/<instance>`, search attributes `BpService`, `BpHook`; расписание `<service>/<Name>`, его запуски — workflow id `<service>/<Name>-<время>`, memo `backplane.service`, `backplane.schedule`; типы application error: `backplane.NoBinding` (нет биндинга), `backplane.HookFailed`, `backplane.Timeout` (вызов хука), `backplane.NonRetryable` (активити); **[backplane]** очередь backplane `backplane`; workflow исполнения биндингов и правил `backplane.Binding.v1`, id вызова хука `binding/<hook>/<Nexus request id>`, тестового запуска биндинга `test/<hook>/<uuid>`, memo `backplane.binding` = `<hook>@<версия>` (`@draft` — несохранённое определение) или `rule:<id>@<версия>`, типы ошибок запуска `backplane.StepFailed`, `backplane.TransformFailed`; Nexus-worker хуков — на той же очереди, только Nexus-задачи; workflow id правила `rule/<id>/<ce-id>`, тестового запуска правила — `test/rule/<id>/<uuid>`; вызовы консоли — workflows `backplane.console.CallHook.v1` (id `hook/<service>/<Name>/console-<uuid>` или `…/<key>`) и `backplane.console.RunActivity.v1` (id `console/activity/<service>/<Name>/<uuid>`) на очереди `backplane`, запуск workflow из консоли — id `console/<service>/<workflow>/<uuid>`, memo `source` = `console:<session>` |
| имена хуков, активити, событий, workflows | `<service>.<Name>`, `Name` — CamelCase `[A-Z][A-Za-z0-9]*`; имя расписания — `[A-Za-z][A-Za-z0-9_]*` |
| **[backplane]** биндинги и правила | биндинг — по полному имени хука; правило — uuid и имя (любой непустой текст); шаг — `[A-Za-z_][A-Za-z0-9_]*`, не `req`, `event`, `meta`, `steps`, не слово CEL, не `on`/`when`; CEL-переменные `req` (биндинг), `event` и `meta` (правило), `<шаг>`, `steps.<шаг>.skipped`; таблицы `binding_version`, `binding_current`, `rule`, `rule_version`, `rule_current`; путь нарушения — `hook`, `event`, `when`, `name`, `steps[<i>].<поле>[.<поле входа>]`, `result[.<поле>]` |
| proto-пакеты | внутреннее API — `<service>.console.v1` (`-` → `_`; другой пакет — ошибка `Run`); конвенция генератора, SDK её не проверяет: хуки `<service>.hooks.v1`, активити `<service>.activities.v1`, события `<service>.events.v1` |
| **[backplane]** консоль | под базой консоли (`/` или `<prefix>/`): `/auth/login`, `/auth/logout`, `/auth/session`, `/ws`, `/plugins/<service>/<hash>/<path>` — бандл, `/s/<service>/...` — плагин в shell; cookie сессии `bp_session`; собственный API — proto-пакет `backplane.console.v1`; relay ставит metadata `bp-console-session` (id сессии) и `bp-internal-secret` |

## 18. Раскладка репозитория

Что есть:

```
backplane/
  platform-design.md         этот документ
  Makefile  easyp.yaml  easyp.lock  sqld.yaml  go.mod  .golangci.yaml
  docker-compose.yaml        platform-in-a-box: Consul, NATS, Temporal (dev), PostgreSQL, Valkey
  deployments/               Dockerfiles, документация деплоя и OTel Collector
  backplanepb/v1/            proto-контракт backplane.v1 (manifest, instance, call) и сгенерированный Go
  backplanepb/console/v1/    API консоли backplane.console.v1 (catalog, session, config, events, workflows,
                             calls, bindings, rules) и сгенерированный Go
  cmd/
    backplane/               бинарь платформы: сервис на своём SDK (имя backplane)
  internal/                  приватное backplane (сервер):
    server/                  конфигурация сервера (§12.2, Validate) и State: дерево узлов — store,
                             registry, дальше компоненты; readiness и объявления сервера
    store/                   PostgreSQL, схема backplane, на infra/postgres: pgxpool + pgtx
                             (транзакции), миграции sqld при старте (migrations/, из diff schema.sql), типизированные запросы
                             (queries/ → db/, sqld-gen-go)
    wire/                    контракт имён SDK и сервера (§8, §9, §17): объекты NATS, заголовки, metadata,
                             redrive-subject, имена Temporal; только стандартная библиотека
    ops/                     операции консоли над стеком (§8, §9): EventService (стримы, consumers и lag,
                             сообщения, тестовые события, dead letters, redrive, purge), WorkflowService,
                             ScheduleService, CallService (workflows вызовов на очереди backplane);
                             NATS и Temporal — клиенты SDK backplane
    registry/                каталог установки из Consul: blocking queries на каталог, health
                             сервисов и backplane/services/; неизменяемые снапшоты Catalog, Source
                             (Current, Changes), Hub — источник снапшотов и фейк для тестов
    config/                  Live-конфигурация (§5.2, §5.3): ревизии override в PostgreSQL, валидация
                             на инстансах схемой, доставка в config/<service>/ (txn + CAS на _revision),
                             reconciler, ConfigService консоли (Manager.API)
    bindings/                биндинги и правила (§7.1, §8.1): модель (шаги по имени, значения —
                             деревья CEL, YAML для демо и тестов), валидация по манифестам с
                             проверкой CEL по схемам (пути JSON Pointer, диапазоны), анализ и
                             переименование шагов, Compile → Program (зависимости, опции,
                             детерминированное вычисление для workflow), версии в PostgreSQL с
                             base_version, BindingService, RuleService и WiringService (Manager)
    executor/                исполнитель хуков (§7.2): Nexus endpoint'ы сервисов с хуками, Nexus-worker
                             <service>.Hooks на очереди backplane (пересобирается при изменении набора
                             хуков), workflow backplane.Binding.v1 (биндинги и правила), RPC запусков
                             BindingService (TestBinding, ListBindingRuns, GetBindingRun, CancelBindingRun)
                             (Build: listener, маршруты, cluster'ы, endpoints), метрики, NACK в лог
    console/                 консоль (§11): компонент со своим listener'ом — вход по
                             admin-токену из конфигурации, сессии (Sessions: PostgreSQL, PG), brute force
                             (Attempts: Valkey),
                             /ws (ws-proto: CatalogService, SessionService, ConfigService, BindingService, RuleService; relay во
                             внутреннее API сервисов), бандлы плагинов с LRU-кэшем, заголовки безопасности
  pkg/backplane/             Go SDK: Open, Root, Service, Identity, Info, опции Open, ErrConfig, ErrClosed
    activity/                активити: Handle, Workflow, опции для биндинга, InfoOf, Heartbeat, NonRetryable
    backplanetest/           harness для тестов компонентов без Open/Run/портов/Consul (§4.6)
    build/                   идентичность из ldflags и build info
    config/                  конфигурация: Load, Open, Runtime, Backplane, Live, Secret, TLS, Validator
    deps/                    дерево узлов: Scope, Component, Dependency, Optional, Singleton,
                             Provider, Factory, Func (без зависимости от пакета backplane)
    event/                   события: Declare, Publish и опции, React и опции доставки, Delivery,
                             Terminal, Redrive, JetStream
    hook/                    хуки без Temporal: Declare, Call, Key, Timeout, ErrUnavailable, ErrNoBinding
    infra/                   подключения к инфраструктуре как провайдеры deps (§4.4): секция
                             конфигурации + New(cfg) deps.Provider[T]; ими пользуется и backplane
      postgres/              PostgreSQL: Config (DSN, лог запросов, пул), DB (pgxpool + pgtx), Open,
                             Schema, Migrations (sqld), Serializable; otelpgx, проба ping
      nats/                  NATS: Config (URL, creds, TLS), Connect (переподключение без конца, без
                             буфера), провайдер со статусом в пробе; через него подключается шина SDK
      temporal/              Temporal: Config (addr, ns, TLS, API key, dial timeout), Options (логгер,
                             OTel-трейсинг и метрики), Dial; через него подключается Temporal SDK
      valkey/                Valkey: Config, Client (valkey-go), OTel, проба PING
    api/                     API declarations, опции managed API, middleware, Origins
    workflows/               workflows автора: Register, Declare, Client, Queue, Schedule и опции
    wsproto/                 ws-proto как managed-роут (своя зависимость ws-proto)
    internal/
      backoff/               политика ретраев (экспоненциальная, с джиттером)
      broker/                события над NATS JetStream: стримы, публикация, реакторы, DLQ, redrive
      configrt/              что ядру нужно от живой конфигурации: эффективные значения, источники,
                             схема, общий Consul-клиент
      consul/                присутствие: манифест, состояние инстанса под сессией, регистрация
      decl/                  общее для объявлений: сервис узла, схемы, кодирование payload
      env/                   общее для узлов одного сервиса: манифест, транспорты, обработчики
      gate/                  гейт внутреннего API: пускает, пока поднято дерево автора
      guard/                 секрет платформенного порта (gRPC и HTTP)
      health/                пробы, grpc.health.v1, HTTP-пробы; один источник для Consul check
      link/                  доступ SDK-пакетов к приватному публичных типов
      listener/              один адрес: gRPC и HTTP через cmux, остановка в бюджете
      manifest/              сборка манифеста, общий FileDescriptorSet, ui.hash
      metrics/               метрики SDK (§15.4)
      node/                  metadata-адаптер дерева xshutdown/lifecycle: имена, telemetry, readiness
      recovery/              паника handler'а — ошибка запроса, backplane.panics
      routes/                описания managed-роутов: префиксы, хосты, перехватчики, политика
      telemetry/             OTel через xtrace: resource, экспортёры из OTEL_*
      temporal/              Temporal: соединение, вызов хуков, worker'ы, активити, TLS
        temporaltest/        тестовая обвязка против dev-сервера: играет Nexus-сторону backplane
      testlog/               логгер для тестов
  examples/
    hello/                   proto/, internal/, cmd/, ui/
    formatter/               второй сервис: Format, Record, реактор Greeted
    demo/cmd/setup/          биндинг и правило через console API
  conformance/               тесты контракта SDK против platform-in-a-box (§16.3)
```

**[backplane]** Целевая дополнительная раскладка для M3:

```
  cmd/
    protoc-gen-backplane/    генератор (удобство)
  internal/
    audit/                   записи и outbox
    obs/                     API телеметрии, драйвер victoria
  web/                       Yarn workspace
    apps/console/            shell (MF-хост)
    apps/catalog/            Storybook и технические сценарии
    packages/                @gopherex/backplane-* (docs/frontend-platform.md)
    templates/module/       самостоятельный UI модуля и embedded entry
```

`pkg/` — только то, что импортируют сервисы; всё остальное Go — в
`internal/`. Корень — документ, сборка, compose.

## 19. Общие библиотеки и эталонная связка

Дерево запуска и остановки SDK использует `xshutdown/lifecycle` v1.1.0.
SDK-узел добавляет имена, logger, scope и readiness; порядок старта,
обратная остановка, бюджеты, горутины и обработка их ошибок принадлежат
библиотеке. Повторная остановка безопасна. `xprobe` v1.2.0 сохраняет причину
неготовности через composite → runner → cached state → HTTP и reporter;
старые status-only пробы и стандартный gRPC health API совместимы.

Nexus endpoint удаляется, если у сервиса непрерывно нет ни инстансов в
каталоге, ни instance-state в Consul в течение
`BACKPLANE_NEXUS_ABSENCE_GRACE` (по умолчанию 5 минут). Период сверки —
`BACKPLANE_NEXUS_RECONCILE_INTERVAL` (1 минута). Перед удалением registry
проверяет Consul согласованными чтениями. Unhealthy-инстанс считается
присутствующим; ошибка Consul откладывает очистку. Удаляются только endpoint'ы
с namespace и task queue этой установки, по версии Temporal. Конкурирующие
реплики сходятся без ошибок на повторном удалении. Манифесты сохраняются;
сами по себе они не восстанавливают удалённый endpoint. Возврат инстанса
восстанавливает endpoint и обслуживание его хуков.

`examples/hello` публикует `Greeted` также после связанного хука, включая
workflow-backed `Welcome`. `examples/formatter` не импортирует hello:
активность `Format` вызывается биндингом `hello.Greet`, `Record` — правилом
на `hello.Greeted`; отдельный reactor читает тот же факт по своей схеме.
`make setup-example` сохраняет связку через API консоли. Сценарии и команды —
`examples/README.md`. `make test-replicas` проверяет эту связку на двух
backplane с общей БД, reconciler, consumer'ами правил и Nexus-worker'ами,
падением/возвратом реплики и уходом/возвратом сервиса.
