import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { expect, userEvent, within } from 'storybook/test';
import * as UI from '@backplane-labs/backplane-ui';

const meta = {
  title: 'Kit/Primitives',
  parameters: { docs: { description: { component: 'Shared primitives in platform tokens. Use the theme toolbar for both modes. These are component fixtures, not console screens.' } } },
} satisfies Meta;
export default meta;
type Story = StoryObj<typeof meta>;
const row = { display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 16 } as const;
const stack = { display: 'grid', gap: 20, maxWidth: 560 };

export const Actions: Story = {
  render: () => <div style={stack}>
    <h1>Actions and status</h1>
    <UI.ButtonGroup aria-label="Actions"><UI.Button>Apply</UI.Button><UI.Button variant="outline">Preview</UI.Button></UI.ButtonGroup>
    <div style={row}><UI.Button disabled>Unavailable</UI.Button><UI.Button aria-busy="true"><UI.Spinner /> Saving</UI.Button><UI.Button variant="destructive">Remove</UI.Button></div>
    <div style={row}><UI.Badge>Active</UI.Badge><UI.Badge variant="secondary">Paused</UI.Badge><UI.Badge variant="outline">Draft</UI.Badge><UI.Kbd>⌘ K</UI.Kbd></div>
    <UI.Alert><UI.AlertTitle>Partial results</UI.AlertTitle><UI.AlertDescription>The source returned a bounded subset. Narrow the query to see the remaining data.</UI.AlertDescription></UI.Alert>
    <UI.Progress value={62} aria-label="Upload progress" />
    <UI.Skeleton style={{ height: 32, width: 240 }} role="status" aria-label="Loading placeholder" />
    <UI.Empty><UI.EmptyHeader><UI.EmptyTitle>No results</UI.EmptyTitle><UI.EmptyDescription>Adjust the filter to include more sources.</UI.EmptyDescription></UI.EmptyHeader></UI.Empty>
  </div>,
};

function InputFixture() {
  const [enabled, setEnabled] = useState(true);
  const [timezone, setTimezone] = useState('utc');
  return <div style={stack}><h1>Form controls</h1>
    <UI.FieldSet><UI.FieldLegend>Connection</UI.FieldLegend><UI.FieldGroup>
      <UI.Field><UI.FieldLabel htmlFor="name">Display name</UI.FieldLabel><UI.Input id="name" defaultValue="hello" /><UI.FieldDescription>Used only in this fixture.</UI.FieldDescription></UI.Field>
      <UI.Field data-invalid><UI.FieldLabel htmlFor="invalid">Invalid identifier</UI.FieldLabel><UI.Input id="invalid" aria-invalid aria-describedby="invalid-error" defaultValue="" /><UI.FieldError id="invalid-error">An identifier is required.</UI.FieldError></UI.Field>
      <UI.Field><UI.FieldLabel htmlFor="notes">Notes</UI.FieldLabel><UI.Textarea id="notes" defaultValue="Local changes" /></UI.Field>
      <UI.Field orientation="horizontal"><UI.Checkbox id="enabled" checked={enabled} onCheckedChange={(value) => setEnabled(value === true)} /><UI.FieldLabel htmlFor="enabled">Enabled</UI.FieldLabel></UI.Field>
      <UI.Field orientation="horizontal"><UI.Switch id="live" defaultChecked /><UI.FieldLabel htmlFor="live">Live updates</UI.FieldLabel></UI.Field>
      <UI.RadioGroup aria-label="Signal" defaultValue="logs"><div style={row}><UI.RadioGroupItem id="logs" value="logs" /><UI.Label htmlFor="logs">Logs</UI.Label><UI.RadioGroupItem id="traces" value="traces" /><UI.Label htmlFor="traces">Traces</UI.Label></div></UI.RadioGroup>
      <UI.Label htmlFor="density">Row height</UI.Label><UI.Slider id="density" defaultValue={[36]} min={24} max={64} aria-label="Row height" />
      <UI.SelectControl aria-label="Timezone" value={timezone} onValueChange={setTimezone} options={[{ value: 'utc', label: 'UTC' }, { value: 'local', label: 'Browser timezone' }]} />
      <UI.InputGroup><UI.InputGroupAddon>https://</UI.InputGroupAddon><UI.InputGroupInput aria-label="Host" placeholder="example.test" /></UI.InputGroup>
    </UI.FieldGroup></UI.FieldSet>
  </div>;
}
export const Inputs: Story = {
  render: InputFixture,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole('checkbox', { name: 'Enabled' }));
    await expect(canvas.getByRole('checkbox', { name: 'Enabled' })).not.toBeChecked();
    await userEvent.click(canvas.getByRole('radio', { name: 'Traces' }));
    await expect(canvas.getByRole('radio', { name: 'Traces' })).toBeChecked();
  },
};

function SelectFixture() {
  const [value, setValue] = useState('');
  return <form aria-label="Select fixture" style={stack}><h1>Select controls</h1>
    <UI.Field><UI.FieldLabel htmlFor="region-choice">Region</UI.FieldLabel>
      <UI.SelectControl id="region-choice" aria-describedby="region-help" name="region" value={value} onValueChange={setValue}
        options={[{ value: '', label: 'All regions' }, { value: 'eu', label: 'Europe' }, { value: 'us', label: 'United States' }, { value: 'disabled', label: 'Unavailable region', disabled: true }, ...Array.from({ length: 40 }, (_, i) => ({ value: `region-${i + 1}`, label: `Region ${i + 1}` }))]} />
      <UI.FieldDescription id="region-help">Choose one region or all regions.</UI.FieldDescription></UI.Field>
    <UI.SelectControl aria-label="Disabled choice" disabled value="eu" onValueChange={() => {}} options={[{ value: 'eu', label: 'Europe' }]} />
    <UI.SelectControl aria-label="Invalid choice" aria-invalid value="missing" placeholder="Choose a region" onValueChange={setValue} options={[{ value: 'eu', label: 'Europe' }]} />
    <output aria-label="Selected region">{value || 'all'}</output>
  </form>;
}
export const SelectControls: Story = { render: SelectFixture };

export const Overlays: Story = {
  render: () => <div style={stack}><h1>Overlays and focus</h1><div style={row}>
    <UI.AlertDialog><UI.AlertDialogTrigger asChild><UI.Button variant="destructive">Remove fixture</UI.Button></UI.AlertDialogTrigger>
      <UI.AlertDialogContent><UI.AlertDialogHeader><UI.AlertDialogTitle>Remove the fixture?</UI.AlertDialogTitle><UI.AlertDialogDescription>This local action demonstrates a confirmation.</UI.AlertDialogDescription></UI.AlertDialogHeader><UI.AlertDialogFooter><UI.AlertDialogCancel>Cancel</UI.AlertDialogCancel><UI.AlertDialogAction>Remove</UI.AlertDialogAction></UI.AlertDialogFooter></UI.AlertDialogContent>
    </UI.AlertDialog>
    <UI.Sheet><UI.SheetTrigger asChild><UI.Button variant="outline">Open details</UI.Button></UI.SheetTrigger><UI.SheetContent><UI.SheetHeader><UI.SheetTitle>Source details</UI.SheetTitle><UI.SheetDescription>Service and instance information.</UI.SheetDescription></UI.SheetHeader><p>hello / instance-01</p></UI.SheetContent></UI.Sheet>
    <UI.Popover><UI.PopoverTrigger asChild><UI.Button variant="outline">Filter</UI.Button></UI.PopoverTrigger><UI.PopoverContent><UI.Label htmlFor="filter-value">Service</UI.Label><UI.Input id="filter-value" defaultValue="hello" /></UI.PopoverContent></UI.Popover>
    <UI.Tooltip><UI.TooltipTrigger asChild><UI.Button variant="ghost">Inspect</UI.Button></UI.TooltipTrigger><UI.TooltipContent>Inspect the selected source</UI.TooltipContent></UI.Tooltip>
    <UI.HoverCard><UI.HoverCardTrigger asChild><a href="#source">hello</a></UI.HoverCardTrigger><UI.HoverCardContent>One healthy instance</UI.HoverCardContent></UI.HoverCard>
  </div></div>,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement), body = within(canvasElement.ownerDocument.body);
    await userEvent.click(canvas.getByRole('button', { name: 'Remove fixture' }));
    await expect(body.getByRole('alertdialog')).toBeVisible();
    await userEvent.click(body.getByRole('button', { name: /^Cancel$/ }));
    await expect(canvas.getByRole('button', { name: 'Remove fixture' })).toHaveFocus();
  },
};

export const Menus: Story = {
  render: () => <div style={stack}><h1>Menus and keyboard navigation</h1>
    <UI.DropdownMenu><UI.DropdownMenuTrigger asChild><UI.Button>Actions</UI.Button></UI.DropdownMenuTrigger><UI.DropdownMenuContent><UI.DropdownMenuLabel>Source</UI.DropdownMenuLabel><UI.DropdownMenuItem>Inspect</UI.DropdownMenuItem><UI.DropdownMenuItem disabled>Remove</UI.DropdownMenuItem><UI.DropdownMenuSeparator /><UI.DropdownMenuCheckboxItem checked>Show timestamps</UI.DropdownMenuCheckboxItem></UI.DropdownMenuContent></UI.DropdownMenu>
    <UI.ContextMenu><UI.ContextMenuTrigger style={{ padding: 24, border: '1px dashed var(--border)' }}>Context menu target</UI.ContextMenuTrigger><UI.ContextMenuContent><UI.ContextMenuItem>Copy identifier</UI.ContextMenuItem><UI.ContextMenuItem>Open trace</UI.ContextMenuItem></UI.ContextMenuContent></UI.ContextMenu>
    <UI.Menubar><UI.MenubarMenu><UI.MenubarTrigger>View</UI.MenubarTrigger><UI.MenubarContent><UI.MenubarItem>Sources</UI.MenubarItem><UI.MenubarItem>Signals</UI.MenubarItem></UI.MenubarContent></UI.MenubarMenu></UI.Menubar>
    <UI.Command><UI.CommandInput aria-label="Find command" placeholder="Find command" /><UI.CommandList><UI.CommandEmpty>No command found.</UI.CommandEmpty><UI.CommandGroup heading="Navigation"><UI.CommandItem>Explore logs</UI.CommandItem><UI.CommandItem>Open trace</UI.CommandItem></UI.CommandGroup></UI.CommandList></UI.Command>
  </div>,
};

export const NavigationAndLayout: Story = {
  render: () => <div style={stack}><h1>Navigation and layout</h1>
    <UI.Breadcrumb><UI.BreadcrumbList><UI.BreadcrumbItem><UI.BreadcrumbLink href="#services">Services</UI.BreadcrumbLink></UI.BreadcrumbItem><UI.BreadcrumbSeparator /><UI.BreadcrumbItem><UI.BreadcrumbPage>hello</UI.BreadcrumbPage></UI.BreadcrumbItem></UI.BreadcrumbList></UI.Breadcrumb>
    <UI.Tabs defaultValue="overview"><UI.TabsList aria-label="Service pages"><UI.TabsTrigger value="overview">Overview</UI.TabsTrigger><UI.TabsTrigger value="config">Configuration</UI.TabsTrigger></UI.TabsList><UI.TabsContent value="overview">Service overview</UI.TabsContent><UI.TabsContent value="config">Configuration form</UI.TabsContent></UI.Tabs>
    <UI.Accordion type="single" collapsible><UI.AccordionItem value="details"><UI.AccordionTrigger>Connection details</UI.AccordionTrigger><UI.AccordionContent>Connected to the standalone fixture</UI.AccordionContent></UI.AccordionItem></UI.Accordion>
    <UI.Card><UI.CardHeader><UI.CardTitle>Service</UI.CardTitle><UI.CardDescription>Standalone fixture</UI.CardDescription></UI.CardHeader><UI.CardContent><UI.Avatar><UI.AvatarFallback>BP</UI.AvatarFallback></UI.Avatar><UI.Separator /><p>Healthy</p></UI.CardContent></UI.Card>
    <UI.ResizablePanelGroup orientation="horizontal" style={{ height: 120 }}><UI.ResizablePanel defaultSize="40%" minSize="20%">Sources</UI.ResizablePanel><UI.ResizableHandle aria-label="Resize panels" withHandle /><UI.ResizablePanel>Details</UI.ResizablePanel></UI.ResizablePanelGroup>
    <UI.Pagination><UI.PaginationContent><UI.PaginationItem><UI.PaginationPrevious href="#previous" /></UI.PaginationItem><UI.PaginationItem><UI.PaginationLink href="#page1" isActive>1</UI.PaginationLink></UI.PaginationItem><UI.PaginationItem><UI.PaginationNext href="#next" /></UI.PaginationItem></UI.PaginationContent></UI.Pagination>
  </div>,
};

export const Calendar: Story = { render: () => <div><h1>Calendar</h1><UI.Calendar mode="single" defaultMonth={new Date(2026, 8, 1)} /></div> };
