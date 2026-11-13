// The words the interface says for the server's codes.

export const callStatus: Record<string, string> = {
  created: 'Набираем',
  active: 'Идёт разговор',
  ended: 'Разговор состоялся',
  failed: 'Не дозвонились',
}

export const result: Record<string, string> = {
  agreed: 'Согласие',
  resolved: 'Вопрос решён',
  agent_left: 'Агент завершил',
  room_gone: 'Связь прервалась',
  declined: 'Отказ',
  callback: 'Просил перезвонить',
  lost_interest: 'Потерял интерес',
  time_up: 'Вышло время',
  caller_left: 'Положил трубку',
  talked: 'Разговор состоялся',
  do_not_call: 'Просил не звонить',
  no_answer: 'Не ответил',
  busy: 'Занято',
  wrong_number: 'Неверный номер',
  stop_list: 'В стоп-листе',
  provider_refused: 'Оператор не принял звонок',
  call_failed: 'Сбой звонка',
  not_started: 'Звонок не начался',
  invalid_data: 'Ошибка в данных строки',
  agent_hung_up: 'Агент завершил',
  lost: 'Звонок потерян',
}

export const targetStatus: Record<string, string> = {
  pending: 'В очереди',
  calling: 'Звоним',
  done: 'Поговорили',
  failed: 'Не дозвонились',
  skipped: 'Пропущен',
}

export const campaignStatus: Record<string, string> = {
  draft: 'Черновик',
  running: 'Идёт',
  paused: 'На паузе',
  done: 'Завершён',
}

export const pauseReason: Record<string, string> = {
  user: 'Остановлен вручную.',
  technical_failures: 'Остановлен сам: в последней порции слишком много технических сбоев звонка. Проверьте линию в кабинете Gapi.',
  do_not_call_requests: 'Остановлен сам: в последней порции каждый пятый или чаще просил больше не звонить. Проверьте, тем ли людям и о том ли звонит агент.',
  agent_did_not_introduce_itself: 'Остановлен сам: в части разговоров агент не представился голосовым ассистентом. Исправьте сценарий и проведите тестовый звонок.',
  balance: 'Остановлен: на балансе Gapi не осталось средств.',
  line: 'Остановлен: линия для звонков не найдена в проекте Gapi.',
  access: 'Остановлен: Gapi не принимает токен проекта или доступ к голосовым агентам закрыт.',
  agent_invalid: 'Остановлен: Gapi не принял сценарий агента. Откройте агента и исправьте замечания.',
}

export const errors: Record<string, string> = {
  signed_out: 'Сессия закончилась. Подключите проект заново.',
  token_invalid: 'Вставьте токен проекта из кабинета Gapi, страница «Токены».',
  token_refused: 'Gapi не принял этот токен.',
  voice_not_available: 'Для этого аккаунта Gapi голосовые агенты пока не открыты.',
  gapi_unreachable: 'Gapi не ответил. Попробуйте ещё раз.',
  tester_phone_needed: 'Сначала укажите номер тестировщика: кому агент позвонит для проверки.',
  phone_invalid: 'Номер пишется так: +998901234567.',
  channels_busy: 'Все каналы заняты звонками. Дождитесь окончания одного из них.',
  test_call_needed: 'У этой версии агента нет принятого тестового звонка. Позвоните тестировщику и примите звонок.',
  call_not_answered: 'Принять можно только состоявшийся и завершённый тестовый звонок.',
  agent_did_not_introduce_itself: 'В этом звонке агент не сказал в первых словах, что он голосовой ассистент. Исправьте сценарий и позвоните ещё раз.',
  line_needed: 'В проекте Gapi не подключена телефонная линия. Подключите её в кабинете Gapi.',
  agent_in_use: 'Этим агентом звонит идущий обзвон. Сначала поставьте его на паузу.',
  phone_column_needed: 'Не удалось определить колонку с номерами. Выберите её.',
  gapi_quota_exceeded: 'На балансе Gapi не осталось средств.',
  gapi_voice_project_limit: 'Проект уже делает столько звонков на телефоны, сколько Gapi разрешает одновременно. Дождитесь окончания одного из них.',
  gapi_voice_no_capacity: 'У Gapi сейчас нет свободного места для звонка. Попробуйте через минуту.',
  gapi_voice_no_runner: 'Голосовой сервис Gapi сейчас недоступен.',
  gapi_voice_line_not_found: 'Линия для звонка не найдена в проекте Gapi.',
  recording_not_ready: 'Запись появляется через несколько секунд после звонка.',
  internal: 'Что-то сломалось на сервере. Попробуйте ещё раз.',
}

export function say(e: unknown): string {
  const err = e as { code?: string; message?: string }
  return (err.code && errors[err.code]) || err.message || 'Ошибка'
}

export const fieldTypes: Record<string, string> = {
  text: 'Текст',
  number: 'Число',
  choice: 'Выбор',
  rating: 'Оценка 1–5',
  yesno: 'Да / нет',
  datetime: 'Дата и время',
}

export const days = ['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс']

export function when(iso: string | null | undefined): string {
  if (!iso) return ''
  return new Date(iso).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Tashkent' })
}

export function seconds(s: number | null | undefined): string {
  if (s == null) return ''
  const n = Math.round(s)
  return `${Math.floor(n / 60)}:${String(n % 60).padStart(2, '0')}`
}
