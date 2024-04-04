# Asynchronous tasks in the service

The service aims to perform the minimum amount of work in the synchronous part of API calls, and offload work to asynchronous tasks.

There are two types of tasks: event-based and periodic.

## Event-based tasks

|          **Trigger**         |                   **Task**                  |
|------------------------------|---------------------------------------------|
| Fleet template updated       | Create new TemplateVersion                  |
| Fleet created                | Create new TemplateVersion                  |
| TemplateVersion created      | Populate the TemplateVersion's Status       |
| TemplateVersion set as valid | Roll out TemplateVersion to Fleet's devices |
| Device created               | Roll out latest TemplateVersion to device   |
| Device owner updated         | Roll out latest TemplateVersion to device   |
| Fleet created                | Update device ownership as necessary        |
| Fleet selector updated       | Update device ownership as necessary        |
| Fleet deleted                | Update device ownership as necessary        |
| Device created               | Update device ownership as necessary        |
| Device labels updated        | Update device ownership as necessary        |
| Device deleted               | Update device ownership as necessary        |

## Periodic tasks

1. Try to access each repository and update its Status.
1. Check if each ResourceSync is up-to-date, and update resources if necessary.