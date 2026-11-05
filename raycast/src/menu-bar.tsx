import { Color, Icon, launchCommand, LaunchType, MenuBarExtra, open } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ago, Briefing, CalendarEvent, clip, CodeTask, dropName, elan, hm, ownerNames, recentEvents, rexDo, shortDate } from "./elan";

const minutesUntil = (iso: string) => Math.round((new Date(iso).getTime() - Date.now()) / 60000);

function nextEvent(events: CalendarEvent[]): CalendarEvent | undefined {
  return events.find((e) => !e.all_day && new Date(e.end).getTime() > Date.now());
}

// "Daily em 8 min", "Daily agora", "Daily 14:00"
function title(e: CalendarEvent | undefined): string | undefined {
  if (!e) return undefined;
  const m = minutesUntil(e.start);
  const name = clip(e.title, 24);
  if (m <= 0) return `${name} agora`;
  if (m <= 60) return `${name} em ${m} min`;
  return `${name} ${hm(e.start)}`;
}

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(
    async () => {
      const [b, code] = await Promise.all([elan<Briefing>("briefing"), elan<CodeTask[]>("codetasks")]);
      return { b, code };
    },
    [],
    { keepPreviousData: true },
  );
  const names = ownerNames();
  const b = data?.b;
  const events = (b?.events ?? []).filter((e) => !e.all_day);
  const next = nextEvent(events);
  const soon = next && minutesUntil(next.start) <= 10;
  const notes = recentEvents(5);

  return (
    <MenuBarExtra
      isLoading={isLoading}
      icon={{ source: "extension-icon.png" }}
      title={title(next)}
      tooltip={next ? `${next.title} · ${hm(next.start)}` : "elan: sem mais reuniões hoje"}
    >
      {next && (
        <MenuBarExtra.Section title="Próxima reunião">
          <MenuBarExtra.Item
            icon={{ source: Icon.Video, tintColor: soon ? Color.Orange : undefined }}
            title={`${hm(next.start)} ${next.title}`}
            subtitle={next.meet_link ? "entrar" : undefined}
            onAction={() => {
              const link = next.meet_link || next.link;
              if (link) open(link);
            }}
          />
          {next.previous && (
            <MenuBarExtra.Item
              icon={Icon.Document}
              title={`Última: ${clip(next.previous.title, 40)} (${shortDate(next.previous.started_at)})`}
              tooltip={next.previous.summary}
              onAction={() => open(`https://docs.google.com/document/d/${next.previous!.id}/edit`)}
            />
          )}
          {(next.my_actions ?? []).slice(0, 5).map((a) => (
            <MenuBarExtra.Item key={a.id} icon={Icon.Circle} title={clip(dropName(a.text, names), 70)} tooltip={a.text} />
          ))}
        </MenuBarExtra.Section>
      )}

      <MenuBarExtra.Section title="Hoje">
        {events.length === 0 && <MenuBarExtra.Item title="nenhuma reunião" />}
        {events.map((e) => {
          const past = new Date(e.end).getTime() < Date.now();
          return (
            <MenuBarExtra.Item
              key={e.id}
              icon={past ? { source: Icon.CheckCircle, tintColor: Color.SecondaryText } : Icon.Calendar}
              title={`${hm(e.start)} ${clip(e.title, 50)}`}
              onAction={() => {
                const link = e.meet_link || e.link;
                if (link) open(link);
              }}
            />
          );
        })}
      </MenuBarExtra.Section>

      {b && (
        <MenuBarExtra.Section title="Pendências">
          <MenuBarExtra.Item
            icon={Icon.List}
            title={`${b.mine_total} suas · ${b.waiting_total} de outros`}
            onAction={() => launchCommand({ name: "pending", type: LaunchType.UserInitiated })}
          />
          {b.mine.slice(0, 5).map((a) => (
            <MenuBarExtra.Item key={a.id} icon={Icon.Circle} title={clip(dropName(a.text, names), 70)} subtitle={ago(a.age_days)} tooltip={a.text} />
          ))}
        </MenuBarExtra.Section>
      )}

      {(data?.code ?? []).length > 0 && (
        <MenuBarExtra.Section title="Code tasks">
          {data!.code.slice(0, 8).map((t) => (
            <MenuBarExtra.Item
              key={t.id}
              icon={t.started_at ? { source: Icon.Play, tintColor: Color.Orange } : Icon.Code}
              title={clip(dropName(t.text, names), 60)}
              subtitle={t.project}
              tooltip={t.folder ? "abre no Rex com o Claude Code" : "projeto sem pasta no config.json"}
              onAction={() => rexDo("elan_code", { id: t.id })}
            />
          ))}
        </MenuBarExtra.Section>
      )}

      {notes.length > 0 && (
        <MenuBarExtra.Section title="Avisos recentes">
          {notes.map((n) => (
            <MenuBarExtra.Item
              key={n.id}
              icon={n.kind === "reminder" ? Icon.Bell : n.kind === "briefing" ? Icon.Sun : Icon.Checkmark}
              title={clip(n.title, 50)}
              subtitle={hm(n.at)}
              tooltip={n.body}
              onAction={n.url ? () => open(n.url!) : undefined}
            />
          ))}
        </MenuBarExtra.Section>
      )}

      {b?.warnings?.map((w) => <MenuBarExtra.Item key={w} icon={{ source: Icon.Warning, tintColor: Color.Orange }} title={clip(w, 70)} />)}

      <MenuBarExtra.Section>
        <MenuBarExtra.Item title="Abrir elan no Rex" icon={Icon.Terminal} onAction={() => rexDo("elan_open", {})} />
        <MenuBarExtra.Item
          title="Buscar no elan…"
          icon={Icon.MagnifyingGlass}
          onAction={() => launchCommand({ name: "search", type: LaunchType.UserInitiated })}
        />
        <MenuBarExtra.Item title="Atualizar" icon={Icon.ArrowClockwise} shortcut={{ modifiers: ["cmd"], key: "r" }} onAction={() => revalidate()} />
      </MenuBarExtra.Section>
    </MenuBarExtra>
  );
}
