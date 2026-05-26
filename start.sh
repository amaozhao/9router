docker stop lazirouter
docker rm lazirouter
docker build -t lazirouter .
docker run -d --name lazirouter -p 20128:20128 --env-file .env -v lazirouter-data:/app/data lazirouter